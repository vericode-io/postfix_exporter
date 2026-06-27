# Deploy: Postfix Exporter + OTel Collector → Grafana Cloud

Esta pasta contém o setup completo para monitorar **múltiplos servidores Postfix** de forma centralizada, com suporte a label `cliente` por servidor e exportação de métricas para o **Grafana Cloud via OTLP**.

## Arquitetura

```
┌─────────────────────────────────────────┐
│  Servidor MX (Postfix roda no HOST)     │
│                                         │
│  [Postfix] ──socket/log──▶              │
│  [postfix_exporter :9154] (Docker)      │
└──────────────────┬──────────────────────┘
                   │  scrape (pull, porta 9154)
                   ▼
┌─────────────────────────────────────────┐
│  Servidor Central                       │
│                                         │
│  [OTel Collector] (Docker)              │
│  · injeta label cliente= por target     │
│  · agrega N servidores MX               │
│           │                             │
│           │  OTLP/HTTP (push)           │
└───────────┼─────────────────────────────┘
            ▼
     Grafana Cloud
```

A label `cliente` **não requer nenhum proxy** no servidor MX. Ela é injetada pelo OTel Collector via `static_configs.labels` no momento do scrape.

---

## Estrutura de arquivos

```
deploy/
├── mx-server/                         # Instalar em cada servidor Postfix
│   ├── docker-compose.yml             # Sobe o postfix_exporter via Docker
│   ├── .env.example                   # Template de variáveis (referência)
│   └── postfix_exporter.service       # Alternativa: systemd sem Docker
│
└── otel-collector/                    # Instalar no servidor central
    ├── docker-compose.yml             # Sobe o OTel Collector via Docker
    ├── otel-collector-config.yaml     # Scrape targets + labels + export OTLP
    └── .env.example                   # Template: credenciais Grafana Cloud
```

---

## Passo 1 — Servidores MX (em cada servidor Postfix)

### Pré-requisito: rede Docker compartilhada

Se o OTel Collector e o `postfix_exporter` rodam no **mesmo servidor**, crie a rede externa uma única vez:

```bash
docker network create monitoring_net
```

> Se estão em **servidores diferentes**, não é necessário criar a rede — a comunicação ocorre via IP normal. Nesse caso, remova a seção `networks` dos dois `docker-compose.yml` e use o IP real do servidor MX no `otel-collector-config.yaml`.

### Opção A: Docker Compose

```bash
cd deploy/mx-server
docker compose up -d

# Verifique se o exporter está respondendo:
curl -s http://localhost:9154/metrics | head -5
```

O Postfix roda no **host** (fora do Docker). O exporter acessa o socket e o log via bind mounts:
- `/var/spool/postfix/public/showq` → socket Unix do Postfix
- `/var/log/mail.log` → arquivo de log

O usuário `root:adm` é necessário para leitura do log (grupo `adm` no Debian/Ubuntu).

### Opção B: Systemd (sem Docker)

```bash
# Instalar o binário
sudo cp postfix_exporter /usr/local/bin/
sudo cp postfix_exporter.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now postfix_exporter

# Verificar:
curl -s http://localhost:9154/metrics | head -5
```

---

## Passo 2 — Servidor Central (OTel Collector)

### 1. Configurar credenciais do Grafana Cloud

```bash
cd deploy/otel-collector
cp .env.example .env
nano .env
# Preencha GRAFANA_OTLP_ENDPOINT e GRAFANA_OTLP_AUTH_BASE64
# Para gerar o Base64: echo -n "INSTANCE_ID:API_KEY" | base64
```

### 2. Adicionar os servidores MX como targets

Edite `otel-collector-config.yaml` e adicione um bloco por servidor:

```yaml
static_configs:
  - targets: ['IP_DO_MX:9154']       # IP real ou nome do serviço Docker
    labels:
      cliente: 'nome-do-cliente'     # label injetada em todas as métricas
      server_name: 'mx-primary'
      datacenter: 'sa-east'
```

### 3. Subir o coletor

```bash
docker compose up -d
docker compose logs -f   # verifique se está enviando sem erros
```

### Como obter as credenciais do Grafana Cloud

1. Acesse seu Grafana Cloud → **Connections → Add new connection → OpenTelemetry**
2. Copie a **URL do endpoint OTLP** (ex: `https://otlp-gateway-prod-us-east-0.grafana.net/otlp`)
3. Gere um **API Token** com escopo `MetricsPublisher`
4. Gere o Base64: `echo -n "SEU_INSTANCE_ID:SEU_API_TOKEN" | base64`

---

## Cenários de rede

| Cenário | Configuração |
|---|---|
| MX e OTel Collector no **mesmo servidor** | Criar rede externa: `docker network create monitoring_net`. Target: `postfix_exporter:9154` (nome do serviço) |
| MX e OTel Collector em **servidores diferentes** | Sem rede compartilhada. Target: `IP_DO_MX:9154`. Remover seção `networks` dos dois composes |

---

## Queries PromQL úteis no Grafana

As métricas chegam com os labels `cliente`, `server_name`, `datacenter` e `instance` preenchidos automaticamente.

| Objetivo | Query |
|---|---|
| Entregas por cliente | `sum by (cliente) (rate(postfix_smtp_messages_processed_total{status="sent"}[5m]))` |
| Rejeições por servidor | `sum by (server_name) (rate(postfix_smtpd_messages_rejected_total[5m]))` |
| Fila ativa total | `sum(postfix_showq_message_size_bytes_count{queue="active"})` |
| Falhas SASL por cliente | `sum by (cliente) (increase(postfix_smtpd_sasl_authentication_failures_total[1h]))` |
| Throughput por datacenter | `sum by (datacenter) (rate(postfix_qmgr_messages_removed_total[5m]))` |
