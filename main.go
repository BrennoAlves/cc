package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	portaPadrao = 8765
	versao      = "0.1.0"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

type Canal struct {
	Nome   string `yaml:"nome"`
	Tipo   string `yaml:"tipo"`
	ChatID string `yaml:"chat_id"`
}

type Config struct {
	Telegram struct {
		Token  string `yaml:"token"`
		ChatID string `yaml:"chat_id"`
	} `yaml:"telegram"`
	NotifyToken string    `yaml:"notify_token"`
	Canais      []Canal   `yaml:"canais"`
	Services    []Servico `yaml:"services"`
	Server      struct {
		CheckInterval    int `yaml:"check_interval"`
		APIPort          int `yaml:"api_port"`
		AlertCooldownMin int `yaml:"alert_cooldown_min"`
		LimiteDiscoPct   int `yaml:"limite_disco_pct"`
		LimiteMemoriaPct int `yaml:"limite_memoria_pct"`
		QuietHours       struct {
			Enabled  bool   `yaml:"enabled"`
			Inicio   int    `yaml:"inicio"`
			Fim      int    `yaml:"fim"`
			Timezone string `yaml:"timezone"`
		} `yaml:"quiet_hours"`
	} `yaml:"server"`
	GCP     *ConfigGCP     `yaml:"gcp"`
	Backups []ConfigBackup `yaml:"backups"`
}

type Servico struct {
	Name      string `yaml:"name"`
	HealthURL string `yaml:"health_url"`
	Channel   string `yaml:"channel"`
	ChatID    string `yaml:"chat_id"`
}

type NotifyPayload struct {
	Project   string `json:"project"`
	Message   string `json:"message"`
	Channel   string `json:"channel"`
	ImagemURL string `json:"imagem_url"`
	Rotina    bool   `json:"rotina"`
}

type Resposta struct {
	OK   bool   `json:"ok"`
	Erro string `json:"erro,omitempty"`
}

func carregarConfig(caminho string) (Config, error) {
	dados, err := os.ReadFile(caminho)
	if err != nil {
		return Config{}, fmt.Errorf("lendo %s: %w", caminho, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(dados, &cfg); err != nil {
		return Config{}, fmt.Errorf("parseando config: %w", err)
	}

	return cfg, nil
}

//acha o canal certo pra entregar a msg
//prioridade: override > canal do servico > chat_id direto > padrao global
func resolverCanal(projeto, override string, cfg Config) (Canal, error) {
	buscarCanal := func(nome string) (Canal, bool) {
		for _, c := range cfg.Canais {
			if c.Nome == nome {
				return c, true
			}
		}
		return Canal{}, false
	}

	if override != "" {
		if c, ok := buscarCanal(override); ok {
			return c, nil
		}
		return Canal{}, fmt.Errorf("canal '%s' não encontrado", override)
	}

	for _, s := range cfg.Services {
		if s.Name == projeto {
			if s.Channel != "" {
				if c, ok := buscarCanal(s.Channel); ok {
					return c, nil
				}
			}
			if s.ChatID != "" {
				return Canal{Tipo: "telegram", ChatID: s.ChatID}, nil
			}
		}
	}

	return Canal{Tipo: "telegram", ChatID: cfg.Telegram.ChatID}, nil
}

func canalPadrao(cfg Config) Canal {
	return Canal{Tipo: "telegram", ChatID: cfg.Telegram.ChatID}
}

//manda a msg pelo canal
//adicionar novos tipos aqui: discord, email, etc
func entregar(canal Canal, cfg Config, msg string) error {
	return entregarComFoto(canal, cfg, msg, "")
}

//igual ao entregar, mas anexa uma foto quando fotoURL nao for vazia
//o texto vai sempre primeiro pra garantir entrega mesmo se a foto falhar
func entregarComFoto(canal Canal, cfg Config, msg, fotoURL string) error {
	switch canal.Tipo {
	case "telegram", "":
		if err := enviarTelegram(cfg.Telegram.Token, canal.ChatID, msg); err != nil {
			return err
		}
		if fotoURL != "" {
			anexarFoto(cfg.Telegram.Token, canal.ChatID, fotoURL)
		}
		return nil
	default:
		return fmt.Errorf("tipo de canal '%s' não implementado", canal.Tipo)
	}
}

//tenta como foto (preview bonito) e cai pra documento se a imagem for grande
//demais pro sendPhoto (limite de 10000px de largura+altura por URL).
//o sendPhoto que falha envenena o cache do telegram pra aquela URL, entao o
//fallback usa a URL "furada" pra forcar uma nova busca.
func anexarFoto(token, chatID, fotoURL string) {
	if err := enviarFotoTelegram(token, chatID, fotoURL); err == nil {
		return
	}
	if err := enviarDocumentoTelegram(token, chatID, comCacheBust(fotoURL)); err != nil {
		log.Printf("notify: texto entregue mas a foto falhou: %v", err)
	}
}

func comCacheBust(u string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%scc=%d", u, sep, time.Now().UnixNano())
}

func enviarTelegram(token, chatID, texto string) error {
	return chamarTelegram(token, "sendMessage", map[string]string{
		"chat_id": chatID,
		"text":    texto,
	})
}

//manda foto via URL publica (preview inline)
func enviarFotoTelegram(token, chatID, fotoURL string) error {
	return chamarTelegram(token, "sendPhoto", map[string]string{
		"chat_id": chatID,
		"photo":   fotoURL,
	})
}

//manda como documento — sem limite de dimensao, preserva resolucao original
func enviarDocumentoTelegram(token, chatID, fotoURL string) error {
	return chamarTelegram(token, "sendDocument", map[string]string{
		"chat_id":  chatID,
		"document": fotoURL,
	})
}

func chamarTelegram(token, metodo string, payload map[string]string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, metodo)

	corpo, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serializando payload: %w", err)
	}

	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(corpo))
	if err != nil {
		return fmt.Errorf("chamando API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram retornou status %d", resp.StatusCode)
	}

	return nil
}

func responderJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func extrairBearer(header string) string {
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}

func handlerNotify(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			responderJSON(w, http.StatusMethodNotAllowed, Resposta{OK: false, Erro: "método não permitido"})
			return
		}

		token := extrairBearer(r.Header.Get("Authorization"))
		if cfg.NotifyToken == "" || token != cfg.NotifyToken {
			responderJSON(w, http.StatusUnauthorized, Resposta{OK: false, Erro: "não autorizado"})
			return
		}

		var payload NotifyPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			responderJSON(w, http.StatusBadRequest, Resposta{OK: false, Erro: "body inválido"})
			return
		}

		if payload.Message == "" {
			responderJSON(w, http.StatusBadRequest, Resposta{OK: false, Erro: "message é obrigatório"})
			return
		}

		canal, err := resolverCanal(payload.Project, payload.Channel, cfg)
		if err != nil {
			responderJSON(w, http.StatusBadRequest, Resposta{OK: false, Erro: err.Error()})
			return
		}

		if payload.Rotina {
			notificarRotina(canal, cfg, payload.Message)
		} else {
			if err := entregarComFoto(canal, cfg, payload.Message, payload.ImagemURL); err != nil {
				responderJSON(w, http.StatusInternalServerError, Resposta{OK: false, Erro: err.Error()})
				return
			}
		}

		responderJSON(w, http.StatusOK, Resposta{OK: true})
	}
}

func iniciarServidor(cfg Config, ctx context.Context) {
	if cfg.Server.APIPort == 0 {
		cfg.Server.APIPort = portaPadrao
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/notify", handlerNotify(cfg))

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Server.APIPort)
	servidor := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	//aguarda o ctx pra fechar sem cortar conexoes no meio
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		servidor.Shutdown(shutCtx)
	}()

	log.Printf("API escutando em %s", addr)

	//ErrServerClosed eh o retorno normal do Shutdown, nao eh erro
	if err := servidor.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("erro no servidor HTTP: %v", err)
	}
}

func msgBoot(cfg Config) string {
	if len(cfg.Services) == 0 {
		return "Oi. Estou aqui, mas você não me deu nada pra observar ainda."
	}

	nomes := make([]string, len(cfg.Services))
	for i, s := range cfg.Services {
		nomes[i] = s.Name
	}

	var lista string
	if len(nomes) == 1 {
		lista = nomes[0]
	} else {
		lista = strings.Join(nomes[:len(nomes)-1], ", ") + " e " + nomes[len(nomes)-1]
	}

	return fmt.Sprintf("Oi. Estou de olho no %s. Pode deixar.", lista)
}

func cmdNotify(args []string) int {
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	project := fs.String("project", "", "nome do projeto")
	message := fs.String("message", "", "mensagem a enviar")
	channel := fs.String("channel", "", "canal de destino (opcional)")
	configPath := fs.String("config", "config.yaml", "arquivo de configuração")
	fs.Parse(args)

	if *message == "" {
		fmt.Fprintln(os.Stderr, "uso: cc notify -message 'texto' [-project nome] [-channel canal] [-config caminho]")
		return 1
	}

	cfg, err := carregarConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "erro ao carregar config: %v\n", err)
		return 1
	}

	canal, err := resolverCanal(*project, *channel, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "erro: %v\n", err)
		return 1
	}

	if err := entregar(canal, cfg, *message); err != nil {
		fmt.Fprintf(os.Stderr, "erro ao enviar: %v\n", err)
		return 1
	}

	fmt.Println("ok")
	return 0
}

func main() {
	//dispatch de subcomandos
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "notify":
			os.Exit(cmdNotify(os.Args[2:]))
		}
	}

	configPath := flag.String("config", "config.yaml", "caminho para o arquivo de configuração")
	flag.Parse()

	cfg, err := carregarConfig(*configPath)
	if err != nil {
		log.Fatalf("erro ao carregar config: %v", err)
	}

	if cfg.Telegram.Token == "" || cfg.Telegram.ChatID == "" {
		log.Fatal("config: telegram.token e telegram.chat_id são obrigatórios")
	}

	if cfg.NotifyToken == "" {
		log.Println("aviso: notify_token não definido — API /notify rejeitará todas as requisições")
	}

	log.Println("cc iniciando...")

	if err := entregar(canalPadrao(cfg), cfg, msgBoot(cfg)); err != nil {
		log.Printf("aviso: mensagem de boot não enviada: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup

	lancar := func(fn func(Config, context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(cfg, ctx)
		}()
	}

	lancar(iniciarServidor)
	lancar(loopHealthcheck)
	lancar(loopServidor)
	lancar(loopGCP)
	lancar(loopBackup)
	lancar(loopEntregaPendentes)

	<-ctx.Done()

	log.Println("cc encerrando...")
	wg.Wait()

	if err := entregar(canalPadrao(cfg), cfg, "Vou sair por um momento."); err != nil {
		log.Printf("aviso: mensagem de shutdown não enviada: %v", err)
	}
}
