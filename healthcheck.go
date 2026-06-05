package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	intervalopadrao  = 60
	cooldownPadrao   = 30
	arquivoEstado    = "state.json"
)

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

type Estado struct {
	Services map[string]EstadoServico `json:"services"`
	Servidor EstadoServidor           `json:"servidor"`
	GCP      EstadoGCP                `json:"gcp"`
}

// nivelAlerta retorna 0 (ok), 70 (aviso) ou 90 (urgente) para um percentual.
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
	semAlerta       acaoAlerta = iota
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
	return Estado{Services: make(map[string]EstadoServico)}
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

	return estado
}

func salvarEstado(caminho string, estado Estado) error {
	dados, err := json.MarshalIndent(estado, "", "  ")
	if err != nil {
		return fmt.Errorf("serializando estado: %w", err)
	}
	return os.WriteFile(caminho, dados, 0644)
}

func verificarServico(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	return nil
}

// processarCheck é uma função pura — recebe o estado anterior e o resultado
// do check, devolve o novo estado e a ação a tomar. Sem efeitos colaterais.
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
			estado := carregarEstado(arquivoEstado)

			for _, s := range cfg.Services {
				err := verificarServico(s.HealthURL)
				anterior := estado.Services[s.Name]
				resultado := processarCheck(anterior, err, cooldown)

				estado.Services[s.Name] = resultado.novoEstado

				chatID := chatIDParaProjeto(s.Name, cfg)

				switch resultado.acao {
				case alertaCaiu:
					enviarTelegram(cfg.Telegram.Token, chatID, msgServicoCaiu(s.Name, resultado.detalhe))
				case alertaAindaFora:
					enviarTelegram(cfg.Telegram.Token, chatID, msgServicoAindaFora(s.Name, anterior.ConsecutiveFailures, anterior.DownSince, resultado.detalhe))
				case alertaRecuperou:
					enviarTelegram(cfg.Telegram.Token, chatID, msgServicoRecuperou(s.Name, resultado.detalhe))
				}
			}

			if err := salvarEstado(arquivoEstado, estado); err != nil {
				log.Printf("erro ao salvar estado: %v", err)
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
