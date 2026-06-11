package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

const (
	limiteEgressPadraoMB = 1024 // 1 GB free tier
	metadataTokenURL     = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	monitoringURL        = "https://monitoring.googleapis.com/v3/projects/%s/timeSeries"
)

type ConfigGCP struct {
	Projeto      string `yaml:"projeto"`
	LimiteEgress int    `yaml:"limite_egress_mb"`
}

func tokenGCP() (string, error) {
	req, err := http.NewRequest("GET", metadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("buscando token GCP: %w", err)
	}
	defer resp.Body.Close()

	var resultado struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resultado); err != nil {
		return "", fmt.Errorf("decodificando token: %w", err)
	}

	return resultado.AccessToken, nil
}

func egressMesAtualMB(projeto, token string) (float64, error) {
	agora := time.Now().UTC()
	inicioMes := time.Date(agora.Year(), agora.Month(), 1, 0, 0, 0, 0, time.UTC)

	params := url.Values{}
	params.Set("filter", `metric.type="compute.googleapis.com/instance/network/sent_bytes_count"`)
	params.Set("interval.startTime", inicioMes.Format(time.RFC3339))
	params.Set("interval.endTime", agora.Format(time.RFC3339))
	params.Set("aggregation.alignmentPeriod", fmt.Sprintf("%ds", int(agora.Sub(inicioMes).Seconds())))
	params.Set("aggregation.perSeriesAligner", "ALIGN_SUM")
	params.Set("aggregation.crossSeriesReducer", "REDUCE_SUM")

	endpoint := fmt.Sprintf(monitoringURL, projeto) + "?" + params.Encode()

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("consultando monitoring API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var resultado struct {
		TimeSeries []struct {
			Points []struct {
				Value struct {
					Int64Value string `json:"int64Value"`
				} `json:"value"`
			} `json:"points"`
		} `json:"timeSeries"`
	}
	if len(body) == 0 {
		return 0, fmt.Errorf("resposta vazia da monitoring API")
	}
	if err := json.Unmarshal(body, &resultado); err != nil {
		return 0, fmt.Errorf("parseando resposta: %w", err)
	}

	var totalBytes int64
	for _, serie := range resultado.TimeSeries {
		for _, ponto := range serie.Points {
			var v int64
			fmt.Sscanf(ponto.Value.Int64Value, "%d", &v)
			totalBytes += v
		}
	}

	return float64(totalBytes) / (1024 * 1024), nil
}

func msgEgressAviso(usadoMB float64, limiteMB int, nivel int) string {
	pct := (usadoMB / float64(limiteMB)) * 100
	sobra := float64(limiteMB) - usadoMB
	if nivel == 90 {
		return fmt.Sprintf("O tráfego de saída do servidor está em %.0f%% do limite — só %.0f MB sobrando esse mês. Cuidado pra não sair do free tier.", pct, sobra)
	}
	return fmt.Sprintf("O tráfego de saída do servidor está em %.0f%% do limite mensal — %.0f MB de %.0f MB usados. Só pra você ficar de olho.", pct, usadoMB, float64(limiteMB))
}

func loopGCP(cfg Config, ctx context.Context) {
	if cfg.GCP == nil {
		return
	}

	limite := cfg.GCP.LimiteEgress
	if limite == 0 {
		limite = limiteEgressPadraoMB
	}

	//uma vez por hora ta bom, egress nao muda rapido
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	log.Printf("monitoramento GCP iniciado (projeto: %s)", cfg.GCP.Projeto)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			token, err := tokenGCP()
			if err != nil {
				log.Printf("GCP: erro ao obter token: %v", err)
				continue
			}

			usadoMB, err := egressMesAtualMB(cfg.GCP.Projeto, token)
			if err != nil {
				log.Printf("GCP: erro ao consultar egress: %v", err)
				continue
			}

			pct := (usadoMB / float64(limite)) * 100
			nivel := nivelAlerta(pct)

			nivelAnterior := lerEstado().GCP.NivelAlertaEgress
			if nivel != nivelAnterior {
				if nivel > nivelAnterior {
					msg := msgEgressAviso(usadoMB, limite, nivel)
					if nivel >= 90 {
						entregar(canalPadrao(cfg), cfg, msg)
					} else {
						notificarRotina(canalPadrao(cfg), cfg, msg)
					}
				}
				atualizarEstado(func(e *Estado) {
					e.GCP.NivelAlertaEgress = nivel
				})
			}
		}
	}
}
