package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	limiteDiscoPadrao   = 85
	limiteMemoriaPadrao = 90
	intervaloServidor   = 60
)

// fstypeIgnorado filtra sistemas de arquivos virtuais e internos do SO.
var fstypeIgnorado = map[string]bool{
	"devfs": true, "autofs": true, "nullfs": true,
	"tmpfs": true, "devtmpfs": true, "sysfs": true,
	"proc": true, "cgroup": true, "overlay": false,
}

type InfoDisco struct {
	Particao string
	Usado    uint64
	Total    uint64
	Percent  float64
}

type InfoMemoria struct {
	Usado   uint64
	Total   uint64
	Percent float64
}

func lerDisco() ([]InfoDisco, error) {
	particoes, err := disk.Partitions(false)
	if err != nil {
		return nil, fmt.Errorf("lendo partições: %w", err)
	}

	vistos := make(map[uint64]bool)
	var resultado []InfoDisco

	for _, p := range particoes {
		if fstypeIgnorado[p.Fstype] {
			continue
		}

		uso, err := disk.Usage(p.Mountpoint)
		if err != nil || uso.Total < 500*1024*1024 {
			continue
		}

		// Evita duplicatas — macOS expõe o mesmo disco em múltiplos mountpoints
		if vistos[uso.Total] {
			continue
		}
		vistos[uso.Total] = true

		resultado = append(resultado, InfoDisco{
			Particao: p.Mountpoint,
			Usado:    uso.Used,
			Total:    uso.Total,
			Percent:  uso.UsedPercent,
		})
	}

	return resultado, nil
}

func lerMemoria() (InfoMemoria, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return InfoMemoria{}, fmt.Errorf("lendo memória: %w", err)
	}

	return InfoMemoria{
		Usado:   v.Used,
		Total:   v.Total,
		Percent: v.UsedPercent,
	}, nil
}

func lerUptime() (time.Duration, error) {
	segundos, err := host.Uptime()
	if err != nil {
		return 0, fmt.Errorf("lendo uptime: %w", err)
	}
	return time.Duration(segundos) * time.Second, nil
}

func fmtBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func msgDiscoCritico(d InfoDisco) string {
	return fmt.Sprintf(
		"O disco do servidor está em %.0f%% — só %s sobrando. Vale limpar alguma coisa.",
		d.Percent, fmtBytes(d.Total-d.Usado),
	)
}

func msgMemoriaCritica(m InfoMemoria) string {
	return fmt.Sprintf(
		"A memória do servidor está em %.0f%%. Não é crítico ainda, mas tá pesado.",
		m.Percent,
	)
}

func loopServidor(cfg Config, ctx context.Context) {
	limDisco := cfg.Server.LimiteDiscoPct
	if limDisco == 0 {
		limDisco = limiteDiscoPadrao
	}

	limMem := cfg.Server.LimiteMemoriaPct
	if limMem == 0 {
		limMem = limiteMemoriaPadrao
	}

	cooldown := time.Duration(cfg.Server.AlertCooldownMin) * time.Minute
	if cooldown == 0 {
		cooldown = time.Duration(cooldownPadrao) * time.Minute
	}

	ticker := time.NewTicker(intervaloServidor * time.Second)
	defer ticker.Stop()

	log.Println("monitoramento do servidor iniciado")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			estado := carregarEstado(arquivoEstado)

			discos, err := lerDisco()
			if err != nil {
				log.Printf("erro ao ler disco: %v", err)
			} else {
				for _, d := range discos {
					if d.Percent >= float64(limDisco) && deveAlertar(estado.Servidor.UltimoAlertaDisco, int(cooldown.Minutes())) {
						enviarTelegram(cfg.Telegram.Token, cfg.Telegram.ChatID, msgDiscoCritico(d))
						estado.Servidor.UltimoAlertaDisco = time.Now().UTC().Format(time.RFC3339)
					}
				}
			}

			mem, err := lerMemoria()
			if err != nil {
				log.Printf("erro ao ler memória: %v", err)
			} else if mem.Percent >= float64(limMem) && deveAlertar(estado.Servidor.UltimoAlertaMemoria, int(cooldown.Minutes())) {
				enviarTelegram(cfg.Telegram.Token, cfg.Telegram.ChatID, msgMemoriaCritica(mem))
				estado.Servidor.UltimoAlertaMemoria = time.Now().UTC().Format(time.RFC3339)
			}

			if err := salvarEstado(arquivoEstado, estado); err != nil {
				log.Printf("erro ao salvar estado: %v", err)
			}
		}
	}
}
