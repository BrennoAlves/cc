package main

import (
	"errors"
	"testing"
	"time"
)

func TestNivelAlerta(t *testing.T) {
	casos := []struct {
		pct  float64
		quer int
	}{
		{0, 0},
		{69.9, 0},
		{70, 70},
		{89.9, 70},
		{90, 90},
		{100, 90},
	}
	for _, c := range casos {
		if got := nivelAlerta(c.pct); got != c.quer {
			t.Errorf("nivelAlerta(%.1f) = %d, quer %d", c.pct, got, c.quer)
		}
	}
}

func TestProcessarCheckPrimeiraFalhaNaoAlerta(t *testing.T) {
	r := processarCheck(EstadoServico{}, errors.New("connection refused"), 30, 2)

	if r.acao != semAlerta {
		t.Errorf("primeira falha com threshold 2 não deve alertar, acao = %v", r.acao)
	}
	if r.novoEstado.Down {
		t.Error("serviço não deve ser marcado Down antes do threshold")
	}
	if r.novoEstado.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, quer 1", r.novoEstado.ConsecutiveFailures)
	}
	if r.novoEstado.DownSince == "" {
		t.Error("DownSince deve ser registrado na primeira falha")
	}
}

func TestProcessarCheckAlertaNoThreshold(t *testing.T) {
	anterior := EstadoServico{ConsecutiveFailures: 1, DownSince: "2026-06-11T00:00:00Z"}
	r := processarCheck(anterior, errors.New("status 500"), 30, 2)

	if r.acao != alertaCaiu {
		t.Errorf("segunda falha com threshold 2 deve gerar alertaCaiu, acao = %v", r.acao)
	}
	if !r.novoEstado.Down {
		t.Error("serviço deve ser marcado Down ao atingir o threshold")
	}
	if r.novoEstado.DownSince != anterior.DownSince {
		t.Error("DownSince da primeira falha deve ser preservado")
	}
}

func TestProcessarCheckBlipResetaSilencioso(t *testing.T) {
	anterior := EstadoServico{ConsecutiveFailures: 1, DownSince: "2026-06-11T00:00:00Z"}
	r := processarCheck(anterior, nil, 30, 2)

	if r.acao != semAlerta {
		t.Errorf("recuperação antes do threshold não deve alertar, acao = %v", r.acao)
	}
	if r.novoEstado.ConsecutiveFailures != 0 || r.novoEstado.DownSince != "" {
		t.Errorf("estado deve zerar após blip, ficou %+v", r.novoEstado)
	}
}

func TestProcessarCheckRecuperacao(t *testing.T) {
	anterior := EstadoServico{Down: true, DownSince: "2026-06-11T00:00:00Z", ConsecutiveFailures: 5}
	r := processarCheck(anterior, nil, 30, 2)

	if r.acao != alertaRecuperou {
		t.Errorf("acao = %v, quer alertaRecuperou", r.acao)
	}
	if r.detalhe != anterior.DownSince {
		t.Errorf("detalhe = %q, quer DownSince anterior", r.detalhe)
	}
	if r.novoEstado.Down {
		t.Error("estado deve zerar após recuperação")
	}
}

func TestProcessarCheckCooldown(t *testing.T) {
	recente := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	anterior := EstadoServico{Down: true, DownSince: recente, LastAlert: recente, ConsecutiveFailures: 3}

	r := processarCheck(anterior, errors.New("ainda fora"), 30, 2)
	if r.acao != semAlerta {
		t.Errorf("dentro do cooldown não deve alertar, acao = %v", r.acao)
	}

	antigo := time.Now().UTC().Add(-45 * time.Minute).Format(time.RFC3339)
	anterior.LastAlert = antigo
	r = processarCheck(anterior, errors.New("ainda fora"), 30, 2)
	if r.acao != alertaAindaFora {
		t.Errorf("após o cooldown deve gerar alertaAindaFora, acao = %v", r.acao)
	}
}

func TestDeveAlertar(t *testing.T) {
	if !deveAlertar("", 30) {
		t.Error("sem LastAlert deve alertar")
	}
	if !deveAlertar("data-invalida", 30) {
		t.Error("LastAlert inválido deve alertar (fail-open)")
	}
}
