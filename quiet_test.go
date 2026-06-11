package main

import "testing"

func TestHoraDentroJanela(t *testing.T) {
	casos := []struct {
		nome              string
		hora, inicio, fim int
		quer              bool
	}{
		{"janela diurna, dentro", 10, 9, 18, true},
		{"janela diurna, antes", 8, 9, 18, false},
		{"janela diurna, no fim (exclusivo)", 18, 9, 18, false},
		{"wrap meia-noite, madrugada", 3, 22, 8, true},
		{"wrap meia-noite, noite", 23, 22, 8, true},
		{"wrap meia-noite, no início", 22, 22, 8, true},
		{"wrap meia-noite, no fim (exclusivo)", 8, 22, 8, false},
		{"wrap meia-noite, tarde", 15, 22, 8, false},
	}
	for _, c := range casos {
		if got := horaDentroJanela(c.hora, c.inicio, c.fim); got != c.quer {
			t.Errorf("%s: horaDentroJanela(%d, %d, %d) = %v, quer %v", c.nome, c.hora, c.inicio, c.fim, got, c.quer)
		}
	}
}

func TestEHorarioQuietoDesabilitado(t *testing.T) {
	var cfg Config
	cfg.Server.QuietHours.Enabled = false
	if eHorarioQuieto(cfg) {
		t.Error("quiet_hours desabilitado nunca deve represar")
	}
}
