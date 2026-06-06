package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	intervaloServidor = 60
)

var fstypeIgnorado = map[string]bool{
	"devfs": true, "autofs": true, "nullfs": true,
	"tmpfs": true, "devtmpfs": true, "sysfs": true,
	"proc": true, "cgroup": true,
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
	return InfoMemoria{Usado: v.Used, Total: v.Total, Percent: v.UsedPercent}, nil
}

func lerCPU() (float64, error) {
	pcts, err := cpu.Percent(3*time.Second, false)
	if err != nil || len(pcts) == 0 {
		return 0, fmt.Errorf("lendo CPU: %w", err)
	}
	return pcts[0], nil
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

func msgDiscoCritico(d InfoDisco, nivel int) string {
	sobra := fmtBytes(d.Total - d.Usado)
	if nivel == 90 {
		return fmt.Sprintf("O disco em %s está crítico — %.0f%% em uso, só %s sobrando. Precisa de atenção agora.", d.Particao, d.Percent, sobra)
	}
	return fmt.Sprintf("O disco em %s está em %.0f%% — %s sobrando. Vale dar uma olhada.", d.Particao, d.Percent, sobra)
}

func msgMemoriaCritica(m InfoMemoria, nivel int) string {
	if nivel == 90 {
		return fmt.Sprintf("A memória está crítica — %.0f%% em uso (%s de %s). Isso pode causar problema.", m.Percent, fmtBytes(m.Usado), fmtBytes(m.Total))
	}
	return fmt.Sprintf("A memória está em %.0f%% — %s de %s em uso. Não é urgente, mas tá pesado.", m.Percent, fmtBytes(m.Usado), fmtBytes(m.Total))
}

func msgCPUCritica(pct float64, nivel int) string {
	if nivel == 90 {
		return fmt.Sprintf("A CPU está em %.0f%%. Alguma coisa está consumindo muito — vale investigar.", pct)
	}
	return fmt.Sprintf("A CPU está em %.0f%%. Ficando pesado, mas ainda ok.", pct)
}

func loopServidor(cfg Config, ctx context.Context) {
	ticker := time.NewTicker(intervaloServidor * time.Second)
	defer ticker.Stop()

	log.Println("monitoramento do servidor iniciado")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			discos, err := lerDisco()
			if err != nil {
				log.Printf("erro ao ler disco: %v", err)
			} else {
				for _, d := range discos {
					nivel := nivelAlerta(d.Percent)
					nivelAnterior := lerEstado().Servidor.NivelAlertaDisco
					if nivel != nivelAnterior {
						if nivel > nivelAnterior {
							entregar(canalPadrao(cfg), cfg, msgDiscoCritico(d, nivel))
						}
						atualizarEstado(func(e *Estado) {
							e.Servidor.NivelAlertaDisco = nivel
						})
					}
				}
			}

			memoria, err := lerMemoria()
			if err != nil {
				log.Printf("erro ao ler memória: %v", err)
			} else {
				nivel := nivelAlerta(memoria.Percent)
				nivelAnterior := lerEstado().Servidor.NivelAlertaMemoria
				if nivel != nivelAnterior {
					if nivel > nivelAnterior {
						entregar(canalPadrao(cfg), cfg, msgMemoriaCritica(memoria, nivel))
					}
					atualizarEstado(func(e *Estado) {
						e.Servidor.NivelAlertaMemoria = nivel
					})
				}
			}

			pctCPU, err := lerCPU()
			if err != nil {
				log.Printf("erro ao ler CPU: %v", err)
			} else {
				nivel := nivelAlerta(pctCPU)
				nivelAnterior := lerEstado().Servidor.NivelAlertaCPU
				if nivel != nivelAnterior {
					if nivel > nivelAnterior {
						entregar(canalPadrao(cfg), cfg, msgCPUCritica(pctCPU, nivel))
					}
					atualizarEstado(func(e *Estado) {
						e.Servidor.NivelAlertaCPU = nivel
					})
				}
			}
		}
	}
}
