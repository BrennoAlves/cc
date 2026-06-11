package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func eHorarioQuieto(cfg Config) bool {
	qh := cfg.Server.QuietHours
	if !qh.Enabled {
		return false
	}
	loc, err := time.LoadLocation(qh.Timezone)
	if err != nil {
		loc = time.UTC
	}
	hora := time.Now().In(loc).Hour()
	if qh.Inicio < qh.Fim {
		return hora >= qh.Inicio && hora < qh.Fim
	}
	// wrap em meia-noite (ex: 22 até 8)
	return hora >= qh.Inicio || hora < qh.Fim
}

// notificarRotina entrega imediatamente fora do quiet_hours ou bufferiza para entregar ao acordar.
func notificarRotina(canal Canal, cfg Config, msg string) {
	if !eHorarioQuieto(cfg) {
		entregar(canal, cfg, msg)
		return
	}
	atualizarEstado(func(e *Estado) {
		e.PendentesRotina = append(e.PendentesRotina, MsgPendente{
			Canal: canal,
			Msg:   msg,
			Em:    time.Now(),
		})
	})
}

func entregarPendentes(cfg Config) {
	var pendentes []MsgPendente
	atualizarEstado(func(e *Estado) {
		if len(e.PendentesRotina) == 0 {
			return
		}
		pendentes = e.PendentesRotina
		e.PendentesRotina = nil
	})

	if len(pendentes) == 0 {
		return
	}

	loc, err := time.LoadLocation(cfg.Server.QuietHours.Timezone)
	if err != nil {
		loc = time.UTC
	}

	// agrupa por canal para entregar um digest por destino
	type grupo struct {
		canal Canal
		msgs  []MsgPendente
	}
	vistos := make(map[string]int)
	var grupos []grupo

	for _, p := range pendentes {
		key := p.Canal.Tipo + ":" + p.Canal.ChatID
		if idx, ok := vistos[key]; ok {
			grupos[idx].msgs = append(grupos[idx].msgs, p)
		} else {
			vistos[key] = len(grupos)
			grupos = append(grupos, grupo{canal: p.Canal, msgs: []MsgPendente{p}})
		}
	}

	for _, g := range grupos {
		if len(g.msgs) == 1 {
			entregar(g.canal, cfg, g.msgs[0].Msg)
			continue
		}
		var linhas []string
		for _, p := range g.msgs {
			hora := p.Em.In(loc).Format("15:04")
			linhas = append(linhas, fmt.Sprintf("• %s — %s", hora, p.Msg))
		}
		digest := "Enquanto você dormia:\n" + strings.Join(linhas, "\n")
		entregar(g.canal, cfg, digest)
	}
}

func loopEntregaPendentes(cfg Config, ctx context.Context) {
	if !cfg.Server.QuietHours.Enabled {
		return
	}

	// entrega pendentes que sobreviveram a um restart
	if !eHorarioQuieto(cfg) {
		entregarPendentes(cfg)
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !eHorarioQuieto(cfg) {
				entregarPendentes(cfg)
			}
		}
	}
}
