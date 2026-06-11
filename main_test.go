package main

import (
	"strings"
	"testing"
)

func configComCanais() Config {
	var cfg Config
	cfg.Telegram.ChatID = "padrao"
	cfg.Canais = []Canal{
		{Nome: "pessoal", Tipo: "telegram", ChatID: "111"},
		{Nome: "app", Tipo: "telegram", ChatID: "222"},
	}
	cfg.Services = []Servico{
		{Name: "com-canal", Channel: "app"},
		{Name: "com-chatid", ChatID: "333"},
	}
	return cfg
}

func TestResolverCanalOverride(t *testing.T) {
	cfg := configComCanais()

	c, err := resolverCanal("qualquer", "pessoal", cfg)
	if err != nil || c.ChatID != "111" {
		t.Errorf("override deve vencer: canal %+v, err %v", c, err)
	}

	if _, err := resolverCanal("qualquer", "inexistente", cfg); err == nil {
		t.Error("override inexistente deve retornar erro")
	}
}

func TestResolverCanalPorServico(t *testing.T) {
	cfg := configComCanais()

	c, _ := resolverCanal("com-canal", "", cfg)
	if c.ChatID != "222" {
		t.Errorf("canal do serviço: ChatID = %q, quer 222", c.ChatID)
	}

	c, _ = resolverCanal("com-chatid", "", cfg)
	if c.ChatID != "333" {
		t.Errorf("chat_id direto do serviço: ChatID = %q, quer 333", c.ChatID)
	}

	c, _ = resolverCanal("desconhecido", "", cfg)
	if c.ChatID != "padrao" {
		t.Errorf("fallback global: ChatID = %q, quer padrao", c.ChatID)
	}
}

func TestExtrairBearer(t *testing.T) {
	if got := extrairBearer("Bearer abc123"); got != "abc123" {
		t.Errorf("extrairBearer = %q, quer abc123", got)
	}
	if got := extrairBearer("Basic abc123"); got != "" {
		t.Errorf("esquema errado deve retornar vazio, got %q", got)
	}
	if got := extrairBearer(""); got != "" {
		t.Errorf("header vazio deve retornar vazio, got %q", got)
	}
}

func TestComCacheBust(t *testing.T) {
	if got := comCacheBust("https://x.com/a.png"); !strings.Contains(got, "?cc=") {
		t.Errorf("URL sem query deve ganhar ?cc=, got %q", got)
	}
	if got := comCacheBust("https://x.com/a.png?v=1"); !strings.Contains(got, "&cc=") {
		t.Errorf("URL com query deve ganhar &cc=, got %q", got)
	}
}

func TestMsgEgressAvisoFormato(t *testing.T) {
	// 512 MB de 1024 MB = 50%
	msg := msgEgressAviso(512, 1024, 70)
	if !strings.Contains(msg, "50%") {
		t.Errorf("percentual errado na mensagem: %q", msg)
	}
	if !strings.Contains(msg, "512 MB de 1024 MB") {
		t.Errorf("valores de MB errados na mensagem: %q", msg)
	}
}
