# cc

Daemon de monitoramento e notificações para servidores Linux. Roda como serviço systemd, entrega alertas via Telegram e expõe uma API HTTP local para que qualquer aplicação no servidor envie notificações.

---

## O que faz

- **Healthcheck** — monitora endpoints HTTP e avisa quando um serviço cai ou volta
- **Servidor** — alerta quando disco, memória ou CPU passam de 70% e 90% (alertas progressivos)
- **GCP free tier** — monitora egress de rede e avisa antes de ultrapassar o limite mensal
- **Backups** — backup automático de bancos SQLite para o Cloud Storage, com rotação por retenção
- **API `/notify`** — recebe notificações de qualquer aplicação via HTTP com autenticação Bearer
- **Subcomando `cc notify`** — envia notificações diretamente pela linha de comando

Tudo entregue via Telegram, com roteamento por projeto via canais nomeados.

---

## Instalação

### Compilar do fonte

```bash
git clone https://github.com/BrennoAlves/cc.git
cd cc
go build -o cc .
```

### Cross-compile para Linux (deploy em servidor)

```bash
GOOS=linux GOARCH=amd64 go build -o cc-linux .
scp cc-linux usuario@servidor:/usr/local/bin/cc
```

Requer Go 1.26+.

---

## Configuração

Copie o exemplo e preencha:

```bash
cp config.example.yaml config.yaml
```

### Estrutura completa do `config.yaml`

```yaml
# Credenciais do bot Telegram (obrigatório)
telegram:
  token: "TOKEN_DO_BOT"    # obtido via @BotFather
  chat_id: "CHAT_ID"       # destino padrão quando nenhum canal específico for encontrado

# Token para autenticar chamadas à API /notify
# Gerar com: openssl rand -hex 32
notify_token: "SEU_TOKEN_AQUI"

# Canais de entrega nomeados
# Cada projeto pode ter seu canal. Tipo "telegram" é o único suportado por ora.
canais:
  - nome: pessoal
    tipo: telegram
    chat_id: "SEU_CHAT_ID"
  - nome: meu-app
    tipo: telegram
    chat_id: "CHAT_ID_DO_APP"

# Serviços monitorados via healthcheck HTTP
services:
  - name: meu-app
    health_url: http://127.0.0.1:8000/health
    channel: meu-app       # referência a um canal acima (opcional)

# Backups automáticos (opcional — remover seção se não usar)
backups:
  - nome: meu-app
    caminho: /caminho/para/banco.db
    bucket: nome-do-bucket-gcs
    retencao_dias: 7
    hora: 3                # hora UTC para rodar (0-23)

# Monitoramento GCP free tier (opcional — remover seção se não usar GCP)
gcp:
  projeto: id-do-projeto-gcp
  limite_egress_mb: 1024   # padrão: 1024 MB (1 GB, limite do free tier)

# Configurações do servidor cc
server:
  check_interval: 60       # segundos entre healthchecks
  api_port: 8765           # porta da API /notify (somente localhost)
  alert_cooldown_min: 30   # minutos entre alertas repetidos de serviço fora
  limite_disco_pct: 85     # percentual de disco que dispara alerta
  limite_memoria_pct: 90   # percentual de memória que dispara alerta
```

---

## Executando

### Como daemon

```bash
./cc -config /etc/cc/config.yaml
```

Ao iniciar, cc envia uma mensagem no Telegram confirmando os serviços monitorados.

### Parar

`Ctrl+C` ou `systemctl stop cc` — envia mensagem de encerramento antes de sair.

---

## Deploy com systemd

```bash
# Copiar o binário
sudo cp cc-linux /usr/local/bin/cc

# Criar diretórios
sudo mkdir -p /etc/cc /var/lib/cc

# Copiar o config
sudo cp config.yaml /etc/cc/config.yaml

# Instalar o serviço
sudo cp deploy/cc.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cc

# Ver logs
journalctl -u cc -f
```

---

## API `/notify`

Endpoint HTTP local para que aplicações no servidor enviem notificações.

### Requisição

```
POST http://127.0.0.1:8765/notify
Authorization: Bearer SEU_TOKEN
Content-Type: application/json

{
  "project": "meu-app",
  "message": "Texto da notificação",
  "channel": "pessoal"     // opcional — sobrescreve o canal do projeto
}
```

### Resposta

```json
{ "ok": true }
{ "ok": false, "erro": "descrição do erro" }
```

---

## Integração

Qualquer linguagem que faça uma requisição HTTP consegue usar o cc.

### curl / shell

```bash
curl -X POST http://localhost:8765/notify \
  -H "Authorization: Bearer SEU_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"project":"meu-app","message":"Deploy concluído"}'
```

### Python

```python
import httpx

httpx.post(
    "http://localhost:8765/notify",
    headers={"Authorization": "Bearer SEU_TOKEN"},
    json={"project": "meu-app", "message": "Erro crítico na rota /foo"},
)
```

### Node.js

```javascript
await fetch("http://localhost:8765/notify", {
  method: "POST",
  headers: {
    "Authorization": "Bearer SEU_TOKEN",
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ project: "meu-app", message: "Job finalizado" }),
});
```

### Subcomando `cc notify`

Para scripts shell e cron jobs, sem precisar construir a requisição HTTP:

```bash
cc notify -message "Backup concluído" -project meu-app
cc notify -message "Alerta crítico" -channel pessoal
cc notify -message "Deploy ok" -project meu-app -config /etc/cc/config.yaml
```

---

## Sistema de canais

Cada projeto pode ter seu canal de destino. Se o projeto não tiver canal configurado, a mensagem vai para o `chat_id` padrão do `telegram`.

```yaml
canais:
  - nome: producao
    tipo: telegram
    chat_id: "-100123456789"   # grupo de produção
  - nome: pessoal
    tipo: telegram
    chat_id: "519431291"       # chat pessoal

services:
  - name: api
    health_url: http://127.0.0.1:3000/health
    channel: producao
  - name: worker
    health_url: http://127.0.0.1:4000/health
    channel: pessoal
```

Novos tipos de canal (Discord, email, etc.) podem ser adicionados implementando o `switch` em `entregar()` no `main.go`.

---

## GCP free tier

Para usar o monitoramento de egress, a service account da VM precisa ter o scope `monitoring` habilitado. Se a VM foi criada sem ele:

```bash
gcloud compute instances stop NOME_DA_VM --zone=ZONA
gcloud compute instances set-service-account NOME_DA_VM --zone=ZONA \
  --scopes=devstorage.read_write,logging.write,monitoring,...
gcloud compute instances start NOME_DA_VM --zone=ZONA
```

---

## Licença

MIT
