package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	intervalopadrao = 60
	cooldownPadrao  = 30
	arquivoEstado   = "state.json"
)

var estadoMu sync.Mutex

type EstadoServico struct {
	Down                bool   `json:"down"`
	DownSince           string `json:"down_since,omitempty"`
	LastAlert           string `json:"last_alert,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
}

type EstadoServidor struct {
	NivelAlertaDisco   int `json:"nivel_alerta_disco"`
	NivelAlertaMemoria int `json:"nivel_alerta_memoria"`
	NivelAlertaCPU     int `json:"nivel_alerta_cpu"`
}

type EstadoGCP struct {
	NivelAlertaEgress int `json:"nivel_alerta_egress"`
}

type MsgPendente struct {
	Canal Canal     `json:"canal"`
	Msg   string    `json:"msg"`
	Em    time.Time `json:"em"`
}

type Estado struct {
	Services        map[string]EstadoServico `json:"services"`
	Servidor        EstadoServidor           `json:"servidor"`
	GCP             EstadoGCP                `json:"gcp"`
	UltimoBackup    map[string]string        `json:"ultimo_backup,omitempty"`
	PendentesRotina []MsgPendente            `json:"pendentes_rotina,omitempty"`
}

func nivelAlerta(pct float64) int {
	if pct >= 90 {
		return 90
	}
	if pct >= 70 {
		return 70
	}
	return 0
}

type acaoAlerta int

const (
	semAlerta acaoAlerta = iota
	alertaCaiu
	alertaAindaFora
	alertaRecuperou
)

type resultadoCheck struct {
	novoEstado EstadoServico
	acao       acaoAlerta
	detalhe    string
}

func estadoVazio() Estado {
	return Estado{
		Services:     make(map[string]EstadoServico),
		UltimoBackup: make(map[string]string),
	}
}

func carregarEstado(caminho string) Estado {
	dados, err := os.ReadFile(caminho)
	if err != nil {
		return estadoVazio()
	}

	var estado Estado
	if err := json.Unmarshal(dados, &estado); err != nil {
		return estadoVazio()
	}

	if estado.Services == nil {
		estado.Services = make(map[string]EstadoServico)
	}
	if estado.UltimoBackup == nil {
		estado.UltimoBackup = make(map[string]string)
	}

	return estado
}

func salvarEstado(caminho string, estado Estado) error {
	dados, err := json.MarshalIndent(estado, "", "  ")
	if err != nil {
		return fmt.Errorf("serializando estado: %w", err)
	}
	return os.WriteFile(caminho, dados, 0600)
}

// atualizarEstado é a única forma correta de modificar o state.json.
// Segura o mutex, carrega, executa fn, salva. Sem race condition.
func atualizarEstado(fn func(*Estado)) error {
	estadoMu.Lock()
	defer estadoMu.Unlock()
	estado := carregarEstado(arquivoEstado)
	fn(&estado)
	return salvarEstado(arquivoEstado, estado)
}

func lerEstado() Estado {
	estadoMu.Lock()
	defer estadoMu.Unlock()
	return carregarEstado(arquivoEstado)
}

func verificarServico(url string) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	return nil
}

//pura: entrada -> saida, sem efeito colateral
func processarCheck(anterior EstadoServico, err error, cooldownMin int) resultadoCheck {
	agora := time.Now().UTC().Format(time.RFC3339)

	if err == nil {
		if anterior.Down {
			return resultadoCheck{
				novoEstado: EstadoServico{},
				acao:       alertaRecuperou,
				detalhe:    anterior.DownSince,
			}
		}
		return resultadoCheck{novoEstado: anterior, acao: semAlerta}
	}

	falhas := anterior.ConsecutiveFailures + 1
	downSince := anterior.DownSince
	if downSince == "" {
		downSince = agora
	}

	novoEstado := EstadoServico{
		Down:                true,
		DownSince:           downSince,
		ConsecutiveFailures: falhas,
		LastAlert:           anterior.LastAlert,
	}

	if !anterior.Down {
		novoEstado.LastAlert = agora
		return resultadoCheck{novoEstado: novoEstado, acao: alertaCaiu, detalhe: err.Error()}
	}

	if deveAlertar(anterior.LastAlert, cooldownMin) {
		novoEstado.LastAlert = agora
		return resultadoCheck{novoEstado: novoEstado, acao: alertaAindaFora, detalhe: err.Error()}
	}

	return resultadoCheck{novoEstado: novoEstado, acao: semAlerta}
}

func deveAlertar(lastAlert string, cooldownMin int) bool {
	if lastAlert == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, lastAlert)
	if err != nil {
		return true
	}
	return time.Since(t) >= time.Duration(cooldownMin)*time.Minute
}

func loopHealthcheck(cfg Config, ctx context.Context) {
	intervalo := cfg.Server.CheckInterval
	if intervalo == 0 {
		intervalo = intervalopadrao
	}

	cooldown := cfg.Server.AlertCooldownMin
	if cooldown == 0 {
		cooldown = cooldownPadrao
	}

	ticker := time.NewTicker(time.Duration(intervalo) * time.Second)
	defer ticker.Stop()

	log.Printf("healthcheck iniciado (intervalo: %ds, cooldown: %dmin)", intervalo, cooldown)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, s := range cfg.Services {
				checkErr := verificarServico(s.HealthURL)
				anterior := lerEstado().Services[s.Name]
				resultado := processarCheck(anterior, checkErr, cooldown)

				atualizarEstado(func(e *Estado) {
					e.Services[s.Name] = resultado.novoEstado
				})

				canal, err := resolverCanal(s.Name, "", cfg)
				if err != nil {
					log.Printf("healthcheck: %v", err)
					continue
				}

				switch resultado.acao {
				case alertaCaiu:
					entregar(canal, cfg, msgServicoCaiu(s.Name, resultado.detalhe))
				case alertaAindaFora:
					entregar(canal, cfg, msgServicoAindaFora(s.Name, anterior.ConsecutiveFailures, anterior.DownSince, resultado.detalhe))
				case alertaRecuperou:
					entregar(canal, cfg, msgServicoRecuperou(s.Name, resultado.detalhe))
				}
			}
		}
	}
}

func fmtAgora() string {
	return time.Now().UTC().Format("02/01 · 15:04 UTC")
}

func fmtDuracao(desde string) string {
	t, err := time.Parse(time.RFC3339, desde)
	if err != nil {
		return "?"
	}

	d := time.Since(t)

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}

	h := int(d.Hours())
	m := int(d.Minutes()) % 60

	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dmin", h, m)
}

func msgServicoCaiu(nome, detalhe string) string {
	return fmt.Sprintf("Oi, o %s caiu às %s. Dá uma olhada quando puder.", nome, fmtAgora())
}

func msgServicoAindaFora(nome string, falhas int, downSince, detalhe string) string {
	return fmt.Sprintf("O %s ainda está fora, faz %s. Só pra você não esquecer.", nome, fmtDuracao(downSince))
}

func msgServicoRecuperou(nome, downSince string) string {
	return fmt.Sprintf("O %s voltou. Ficou fora por %s, tudo certo agora.", nome, fmtDuracao(downSince))
}
