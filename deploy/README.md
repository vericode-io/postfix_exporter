# Deploy: Postfix Exporter + Label Proxy + OTel Collector → Grafana Cloud

Esta pasta contém o setup completo para monitorar **múltiplos servidores Postfix** de forma centralizada, com suporte a label `cliente` por servidor e exportação de métricas para o **Grafana Cloud via OTLP**.

## Arquitetura

```
┌─────────────────────────────────────┐
│         Servidor MX (Postfix)       │
│                                     │
│  postfix_exporter :9154 (loopback)  │
│           │                         │
│  prom-label-proxy :9155  ◀──────────┼── scrape (OTel Collector)
│  (injeta label cliente=X)           │
└─────────────────────────────────────┘
                  ▲
                  │  scrape (pull, porta 9155)
                  │
┌─────────────────────────────────────┐
│       Servidor Central              │
│                                     │
│       OTel Collector                │
│  (agrega N servidores MX)           │
│           │                         │
│           │  OTLP/HTTP (push)       │
└───────────┼─────────────────────────┘
            ▼
     Grafana Cloud
```

O `postfix_exporter` **não suporta labels customizados nativamente**. O `prom-label-proxy` resolve isso interceptando o scrape e injetando o label `cliente` em todas as métricas antes de repassá-las ao coletor. O exporter fica restrito ao loopback (`127.0.0.1:9154`) e o proxy é o único ponto de acesso externo (`:9155`).

---

## Estrutura de arquivos

```
deploy/
├── mx-server/                         # Instalar em cada servidor Postfix
│   ├── docker-compose.yml             # Sobe exporter + proxy via Docker
│   ├── .env.example                   # Template: define CLIENTE=nome-do-cliente
│   ├── postfix_exporter.service       # Alternativa systemd para o exporter
│   └── postfix_label_proxy.service    # Alternativa systemd para o proxy
│
└── otel-collector/                    # Instalar no servidor central
    ├── docker-compose.yml             # Sobe o OTel Collector via Docker
    ├── otel-collector-config.yaml     # Configuração: scrape targets + export OTLP
    └── .env.example                   # Template: credenciais Grafana Cloud
```

---

## Passo 1 — Servidores MX (em cada servidor Postfix)

### Opção A: Docker Compose

```bash
cd deploy/mx-server
cp .env.example .env
# Edite .env e defina o nome do cliente:
echo "CLIENTE=acme-corp" > .env
docker-compose up -d
# Verifique se a label está sendo injetada:
curl -s http://localhost:9155/metrics | grep 'cliente='
```

### Opção B: Systemd (sem Docker)

```bash
# 1. Instalar o binário do prom-label-proxy
VERSAO=0.11.0
curl -L "https://github.com/prometheus-community/prom-label-proxy/releases/download/v${VERSAO}/prom-label-proxy_${VERSAO}_linux_amd64.tar.gz" \
  | tar xz && sudo mv prom-label-proxy /usr/local/bin/

# 2. Editar o valor CLIENTE no arquivo .service
sudo nano postfix_label_proxy.service   # altere Environment="CLIENTE=nome-do-cliente"

# 3. Instalar e ativar ambas as units
sudo cp postfix_exporter.service postfix_label_proxy.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now postfix_exporter postfix_label_proxy
```

---

## Passo 2 — Servidor Central (OTel Collector)

```bash
cd deploy/otel-collector

# 1. Configurar credenciais do Grafana Cloud
cp .env.example .env
nano .env
# Preencha GRAFANA_OTLP_ENDPOINT e GRAFANA_OTLP_AUTH_BASE64
# Para gerar o Base64: echo -n "INSTANCE_ID:API_KEY" | base64

# 2. Adicionar os IPs dos servidores MX
nano otel-collector-config.yaml
# Em static_configs, substitua os IPs de exemplo pelos seus servidores reais.
# Cada entrada pode ter labels customizados (server_name, datacenter, etc.)

# 3. Subir o coletor
docker-compose up -d
docker-compose logs -f   # verifique se está enviando sem erros
```

### Como obter as credenciais do Grafana Cloud

1. Acesse seu Grafana Cloud → **Connections → Add new connection → OpenTelemetry**
2. Copie a **URL do endpoint OTLP** (ex: `https://otlp-gateway-prod-us-east-0.grafana.net/otlp`)
3. Gere um **API Token** com escopo `MetricsPublisher`
4. Gere o Base64: `echo -n "SEU_INSTANCE_ID:SEU_API_TOKEN" | base64`

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
