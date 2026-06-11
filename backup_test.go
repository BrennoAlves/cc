package main

import (
	"reflect"
	"testing"
	"time"
)

func TestBackupsExpirados(t *testing.T) {
	corte := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC) // retenção de 7 dias a partir de 11/06

	objetos := []string{
		"backups/meu-app_20260601_030000.db.gz", // expirado
		"backups/meu-app_20260603_030000.db.gz", // expirado
		"backups/meu-app_20260604_030000.db.gz", // exatamente no corte: mantém
		"backups/meu-app_20260610_030000.db.gz", // recente
		"backups/sem-data.db.gz",                // nome fora do padrão: ignora
		"backups/meu-app_naoedata_030000.db.gz", // data inválida: ignora
	}

	quer := []string{
		"backups/meu-app_20260601_030000.db.gz",
		"backups/meu-app_20260603_030000.db.gz",
	}

	got := backupsExpirados(objetos, corte)
	if !reflect.DeepEqual(got, quer) {
		t.Errorf("backupsExpirados = %v, quer %v", got, quer)
	}
}
