package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type ConfigBackup struct {
	Nome         string `yaml:"nome"`
	Caminho      string `yaml:"caminho"`
	Bucket       string `yaml:"bucket"`
	RetencaoDias int    `yaml:"retencao_dias"`
	Hora         int    `yaml:"hora"`
}

func copiarArquivo(origem, destino string) error {
	src, err := os.Open(origem)
	if err != nil {
		return fmt.Errorf("abrindo origem: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(destino)
	if err != nil {
		return fmt.Errorf("criando destino: %w", err)
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func comprimirArquivo(origem, destino string) error {
	src, err := os.Open(origem)
	if err != nil {
		return fmt.Errorf("abrindo arquivo: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(destino)
	if err != nil {
		return fmt.Errorf("criando arquivo gzip: %w", err)
	}
	defer dst.Close()

	gz := gzip.NewWriter(dst)
	defer gz.Close()

	_, err = io.Copy(gz, src)
	return err
}

func uploadGCS(caminho, bucket, nome, token string) error {
	arquivo, err := os.Open(caminho)
	if err != nil {
		return fmt.Errorf("abrindo arquivo para upload: %w", err)
	}
	defer arquivo.Close()

	url := fmt.Sprintf(
		"https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=media&name=backups/%s",
		bucket, nome,
	)

	req, err := http.NewRequest("POST", url, arquivo)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload falhou: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("GCS retornou status %d", resp.StatusCode)
	}

	return nil
}

func listarBackupsGCS(bucket, prefixo, token string) ([]string, error) {
	url := fmt.Sprintf(
		"https://storage.googleapis.com/storage/v1/b/%s/o?prefix=backups/%s",
		bucket, prefixo,
	)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var resultado struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	json.NewDecoder(resp.Body).Decode(&resultado)

	nomes := make([]string, len(resultado.Items))
	for i, item := range resultado.Items {
		nomes[i] = item.Name
	}
	return nomes, nil
}

func deletarObjetoGCS(bucket, nome, token string) error {
	url := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s",
		bucket, strings.ReplaceAll(nome, "/", "%2F"))

	req, _ := http.NewRequest("DELETE", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func rotacionarBackups(bucket, prefixo, token string, retencaoDias int) {
	objetos, err := listarBackupsGCS(bucket, prefixo, token)
	if err != nil {
		log.Printf("backup: erro ao listar objetos para rotação: %v", err)
		return
	}

	corte := time.Now().UTC().AddDate(0, 0, -retencaoDias)

	for _, obj := range objetos {
		partes := strings.Split(obj, "_")
		if len(partes) < 2 {
			continue
		}
		dataStr := partes[len(partes)-2]
		t, err := time.Parse("20060102", dataStr)
		if err != nil {
			continue
		}
		if t.Before(corte) {
			deletarObjetoGCS(bucket, obj, token)
		}
	}
}

func realizarBackup(b ConfigBackup, token string) error {
	timestamp := time.Now().UTC().Format("20060102_150405")
	nomeArquivo := fmt.Sprintf("%s_%s.db.gz", b.Nome, timestamp)

	tmpDB := fmt.Sprintf("/tmp/%s_%s.db", b.Nome, timestamp)
	tmpGZ := fmt.Sprintf("/tmp/%s", nomeArquivo)

	defer os.Remove(tmpDB)
	defer os.Remove(tmpGZ)

	if err := copiarArquivo(b.Caminho, tmpDB); err != nil {
		return fmt.Errorf("copiando banco: %w", err)
	}

	if err := comprimirArquivo(tmpDB, tmpGZ); err != nil {
		return fmt.Errorf("comprimindo: %w", err)
	}

	if err := uploadGCS(tmpGZ, b.Bucket, nomeArquivo, token); err != nil {
		return fmt.Errorf("upload GCS: %w", err)
	}

	retencao := b.RetencaoDias
	if retencao == 0 {
		retencao = 7
	}
	rotacionarBackups(b.Bucket, b.Nome, token, retencao)

	return nil
}

func loopBackup(cfg Config, ctx context.Context) {
	if len(cfg.Backups) == 0 {
		return
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	log.Printf("backup iniciado (%d configurado(s))", len(cfg.Backups))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			agora := time.Now().UTC()
			hoje := agora.Format("2006-01-02")

			for _, b := range cfg.Backups {
				if agora.Hour() != b.Hora {
					continue
				}

				if lerEstado().UltimoBackup[b.Nome] == hoje {
					continue
				}

				token, err := tokenGCP()
				if err != nil {
					log.Printf("backup %s: erro ao obter token: %v", b.Nome, err)
					continue
				}

				if err := realizarBackup(b, token); err != nil {
					log.Printf("backup %s: %v", b.Nome, err)
					entregar(canalPadrao(cfg), cfg, fmt.Sprintf("O backup do %s falhou.\n\n%v", b.Nome, err))
				} else {
					log.Printf("backup %s: concluído", b.Nome)
					entregar(canalPadrao(cfg), cfg, fmt.Sprintf("Backup do %s concluído.", b.Nome))
					atualizarEstado(func(e *Estado) {
						e.UltimoBackup[b.Nome] = hoje
					})
				}
			}
		}
	}
}
