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
	"syscall"

	"gopkg.in/yaml.v3"
)

const (
	portaPadrao = 8765
	timeoutHTTP = 10
	versao      = "0.1.0"
)

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
	Project string `json:"project"`
	Message string `json:"message"`
	Channel string `json:"channel"`
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
func resolverCanal(projeto, override string, cfg Config) Canal {
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
			return c
		}
	}

	for _, s := range cfg.Services {
		if s.Name == projeto {
			if s.Channel != "" {
				if c, ok := buscarCanal(s.Channel); ok {
					return c
				}
			}
			if s.ChatID != "" {
				return Canal{Tipo: "telegram", ChatID: s.ChatID}
			}
		}
	}

	return Canal{Tipo: "telegram", ChatID: cfg.Telegram.ChatID}
}

func canalPadrao(cfg Config) Canal {
	return Canal{Tipo: "telegram", ChatID: cfg.Telegram.ChatID}
}

//manda a msg pelo canal
//adicionar novos tipos aqui: discord, email, etc
func entregar(canal Canal, cfg Config, msg string) error {
	switch canal.Tipo {
	case "telegram", "":
		return enviarTelegram(cfg.Telegram.Token, canal.ChatID, msg)
	default:
		return fmt.Errorf("tipo de canal '%s' não implementado", canal.Tipo)
	}
}

func enviarTelegram(token, chatID, texto string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	corpo, err := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    texto,
	})
	if err != nil {
		return fmt.Errorf("serializando payload: %w", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(corpo))
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

		canal := resolverCanal(payload.Project, payload.Channel, cfg)

		if err := entregar(canal, cfg, payload.Message); err != nil {
			responderJSON(w, http.StatusInternalServerError, Resposta{OK: false, Erro: err.Error()})
			return
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
		servidor.Shutdown(context.Background())
	}()

	log.Printf("API escutando em %s", addr)

	//ErrServerClosed é o retorno normal do Shutdown, nao é erro
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

	canal := resolverCanal(*project, *channel, cfg)
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

	go iniciarServidor(cfg, ctx)
	go loopHealthcheck(cfg, ctx)
	go loopServidor(cfg, ctx)
	go loopGCP(cfg, ctx)
	go loopBackup(cfg, ctx)

	<-ctx.Done()

	log.Println("cc encerrando...")

	if err := entregar(canalPadrao(cfg), cfg, "Vou sair por um momento."); err != nil {
		log.Printf("aviso: mensagem de shutdown não enviada: %v", err)
	}
}
