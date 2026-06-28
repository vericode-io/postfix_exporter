package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers locais
// ---------------------------------------------------------------------------

// collectAllMetrics coleta todas as métricas de um Collector e as retorna
// como um mapa indexado por nome completo da métrica.
func collectAllMetrics(t *testing.T, e *PostfixExporter) map[string][]*io_prometheus_client.Metric {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(e))

	mfs, err := reg.Gather()
	require.NoError(t, err)

	result := make(map[string][]*io_prometheus_client.Metric)
	for _, mf := range mfs {
		result[mf.GetName()] = mf.GetMetric()
	}
	return result
}

// metricSum soma os valores de todos os time series de uma métrica pelo nome.
func metricSum(metrics map[string][]*io_prometheus_client.Metric, name string) float64 {
	var total float64
	for _, m := range metrics[name] {
		if m.Counter != nil {
			total += m.Counter.GetValue()
		} else if m.Gauge != nil {
			total += m.Gauge.GetValue()
		} else if m.Histogram != nil {
			total += float64(m.Histogram.GetSampleCount())
		}
	}
	return total
}

// metricSumByLabel soma os valores de time series filtrados por um label específico.
func metricSumByLabel(metrics map[string][]*io_prometheus_client.Metric, name, labelName, labelValue string) float64 {
	var total float64
	for _, m := range metrics[name] {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == labelName && lp.GetValue() == labelValue {
				if m.Counter != nil {
					total += m.Counter.GetValue()
				} else if m.Gauge != nil {
					total += m.Gauge.GetValue()
				}
			}
		}
	}
	return total
}

// ---------------------------------------------------------------------------
// Teste com arquivo de log de fixture (mail.log.sample)
// ---------------------------------------------------------------------------

// TestCollectFromSampleLogFile testa o parsing do arquivo de log de exemplo
// incluído em testdata/mail.log.sample. Este arquivo contém timestamps antigos
// (passados), o que é intencional: o CollectFromLogLine ignora completamente
// o timestamp — apenas o conteúdo da mensagem é processado.
func TestCollectFromSampleLogFile(t *testing.T) {
	const fixtureFile = "testdata/mail.log.sample"

	f, err := os.Open(fixtureFile)
	require.NoError(t, err, "arquivo de fixture não encontrado: %s", fixtureFile)
	defer f.Close()

	e := newTestExporter(t)

	var linesProcessed int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Ignorar linhas de comentário e linhas vazias
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		e.CollectFromLogLine(line)
		linesProcessed++
	}
	require.NoError(t, scanner.Err())
	require.Greater(t, linesProcessed, 0, "nenhuma linha foi processada — verifique o arquivo de fixture")

	t.Logf("Linhas processadas: %d", linesProcessed)

	// Verificações baseadas no conteúdo do mail.log.sample
	// Estas assertions são adaptadas automaticamente ao arquivo real:
	// se o arquivo real tiver mais eventos, os valores serão maiores.

	assert.GreaterOrEqual(t, counterValue(t, e.cleanupProcesses), 1.0,
		"cleanup: ao menos 1 mensagem processada esperada")

	assert.GreaterOrEqual(t, counterValue(t, e.qmgrRemoves), 1.0,
		"qmgr: ao menos 1 mensagem removida esperada")

	assert.GreaterOrEqual(t, counterValue(t, e.smtpdConnects), 1.0,
		"smtpd: ao menos 1 conexão esperada")

	assert.GreaterOrEqual(t, counterValue(t, e.smtpdDisconnects), 1.0,
		"smtpd: ao menos 1 desconexão esperada")

	assert.GreaterOrEqual(t, counterValue(t, e.bounceNonDelivery), 1.0,
		"bounce: ao menos 1 NDN esperada")

	assert.GreaterOrEqual(t, counterValue(t, e.virtualDelivered), 1.0,
		"virtual: ao menos 1 entrega esperada")

	assert.GreaterOrEqual(t, counterVecTotal(t, e.opendkimSignatureAdded), 1.0,
		"opendkim: ao menos 1 assinatura DKIM esperada")
}

// ---------------------------------------------------------------------------
// Teste com arquivo de log REAL fornecido pelo usuário
// ---------------------------------------------------------------------------

// TestCollectFromRealLogFile testa o parsing de um arquivo de log real do Postfix.
// Para usar este teste:
//
//  1. Faça upload do arquivo de log para testdata/mail.log.real
//     (pode conter timestamps antigos — o exporter os ignora completamente)
//
//  2. Execute apenas este teste:
//     go test -v -run TestCollectFromRealLogFile ./...
//
// O teste valida que:
//   - O arquivo é lido sem erros
//   - Ao menos uma linha é reconhecida como evento Postfix válido
//   - As métricas principais têm valores > 0 (se o log contiver os eventos correspondentes)
//   - A taxa de linhas não reconhecidas é inferior a 50% do total
func TestCollectFromRealLogFile(t *testing.T) {
	const realLogFile = "testdata/mail.log.real"

	if _, err := os.Stat(realLogFile); os.IsNotExist(err) {
		t.Skipf("arquivo de log real não encontrado: %s\n"+
			"Faça upload do arquivo para este caminho e execute novamente.\n"+
			"Timestamps antigos são aceitos — o exporter os ignora.", realLogFile)
	}

	f, err := os.Open(realLogFile)
	require.NoError(t, err)
	defer f.Close()

	e := newTestExporter(t)

	var totalLines, commentLines int
	scanner := bufio.NewScanner(f)
	// Buffer maior para linhas longas de log
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		totalLines++
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			commentLines++
			continue
		}
		e.CollectFromLogLine(line)
	}
	require.NoError(t, scanner.Err())

	dataLines := totalLines - commentLines
	require.Greater(t, dataLines, 0, "arquivo de log está vazio ou contém apenas comentários")

	t.Logf("Total de linhas: %d (dados: %d, comentários/vazias: %d)",
		totalLines, dataLines, commentLines)

	// Coletar todas as métricas para análise
	metrics := collectAllMetrics(t, e)

	// Contar linhas não reconhecidas
	unsupportedTotal := metricSum(metrics, "postfix_unsupported_log_entries_total")
	recognizedLines := float64(dataLines) - unsupportedTotal

	t.Logf("Linhas reconhecidas: %.0f / %d (%.1f%%)",
		recognizedLines, dataLines,
		recognizedLines/float64(dataLines)*100)

	// Imprimir resumo de todas as métricas com valor > 0
	t.Log("=== Resumo das métricas coletadas ===")
	for name := range metrics {
		sum := metricSum(metrics, name)
		if sum > 0 {
			t.Logf("  %-65s = %.0f", name, sum)
		}
	}

	// Imprimir detalhes de smtpd_rejects por código de rejeição
	if rejects, ok := metrics["postfix_smtpd_messages_rejected_total"]; ok {
		t.Log("=== Rejeições SMTPD por código ===")
		for _, m := range rejects {
			var code, service string
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case "code":
					code = lp.GetValue()
				case "service":
					service = lp.GetValue()
				}
			}
			if m.Counter.GetValue() > 0 {
				t.Logf("  código=%s service=%s count=%.0f", code, service, m.Counter.GetValue())
			}
		}
	}

	// Imprimir entregas SMTP por status
	if smtpProc, ok := metrics["postfix_smtp_messages_processed_total"]; ok {
		t.Log("=== Entregas SMTP por status ===")
		for _, m := range smtpProc {
			var status string
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "status" {
					status = lp.GetValue()
				}
			}
			if m.Counter.GetValue() > 0 {
				t.Logf("  status=%-12s count=%.0f", status, m.Counter.GetValue())
			}
		}
	}

	// Validações mínimas — o arquivo deve conter ao menos algum evento Postfix
	assert.Greater(t, recognizedLines, 0.0,
		"nenhuma linha foi reconhecida como evento Postfix válido — verifique se o arquivo é um mail.log do Postfix")

	// A taxa de linhas não reconhecidas deve ser inferior a 50%
	unrecognizedRate := unsupportedTotal / float64(dataLines)
	assert.Less(t, unrecognizedRate, 0.50,
		fmt.Sprintf("taxa de linhas não reconhecidas muito alta: %.1f%% (%.0f de %d linhas)\n"+
			"Isso pode indicar um formato de log diferente do esperado pelo exporter.",
			unrecognizedRate*100, unsupportedTotal, dataLines))
}

// ---------------------------------------------------------------------------
// Teste de regressão: timestamps antigos não afetam o parsing
// ---------------------------------------------------------------------------

// TestNestedSubprocessAndISO8601Timestamp valida o suporte a subprocessos aninhados
// (postfix/polite/smtp, postfix/discard, etc.) e ao formato de timestamp ISO 8601
// gerado pelo journald/rsyslog moderno, como encontrado nos logs reais do cliente.
func TestNestedSubprocessAndISO8601Timestamp(t *testing.T) {
	e := newTestExporter(t)

	// Linhas com timestamp ISO 8601 e subprocessos aninhados (do log real)
	lines := []struct {
		line    string
		desc    string
	}{
		// postfix/polite/smtp → deve ser tratado como smtp (último componente)
		{`2026-06-14T18:08:39.620674-03:00 mx3 postfix/polite/smtp[107825]: 4gdgQw15: to=<a@gmail.com>, relay=alt1.gmail-smtp-in.l.google.com[1.2.3.4]:25, delay=12736, delays=802/11925/7/1.7, dsn=4.7.23, status=deferred (host said: 421 rate limited)`, "ISO8601 + polite/smtp deferred"},
		{`2026-06-14T18:08:39.690814-03:00 mx3 postfix/polite/smtp[107846]: 4gdfJL6: to=<b@gmail.com>, relay=gmail-smtp-in.l.google.com[1.2.3.4]:25, delay=15782, delays=3848/11930/1/2.8, dsn=2.0.0, status=sent (250 OK)`, "ISO8601 + polite/smtp sent"},
		{`2026-06-14T18:08:39.844812-03:00 mx3 postfix/polite/smtp[107967]: 4gdgYd: to=<c@gmail.com>, relay=gmail-smtp-in.l.google.com[1.2.3.4]:25, delay=12387, delays=452/11929/1.1/4.4, dsn=5.7.1, status=bounced (host said: 550 spam)`, "ISO8601 + polite/smtp bounced"},
		// postfix/ultramegaturtle/smtp → deve ser tratado como smtp
		{`2026-06-14T18:08:40.631989-03:00 mx3 postfix/ultramegaturtle/smtp[107960]: 4gddZV: to=<d@uol.com.br>, relay=mx.uol.com.br[1.2.3.4]:25, delay=17751, delays=14956/2792/2.1/0.54, dsn=4.7.1, status=deferred (blacklisted)`, "ISO8601 + ultramegaturtle/smtp"},
		// postfix/discard → subprocess = discard (não mapeado → unsupported, mas não deve crashar)
		{`2026-06-14T18:08:40.293232-03:00 mx3 postfix/discard[107974]: 4gcYdn: to=<e@faturaporto.com.br>, relay=none, delay=169002, delays=0.01/169002/0/0, dsn=2.0.0, status=sent (faturaporto.com.br)`, "ISO8601 + discard"},
		// Timestamp syslog clássico com subprocesso aninhado
		{`Jun 14 18:08:39 mx3 postfix/polite/smtp[107825]: 4gdgQw15: to=<f@gmail.com>, relay=alt1.gmail-smtp-in.l.google.com[1.2.3.4]:25, delay=100, delays=10/80/5/5, dsn=2.0.0, status=sent (250 OK)`, "syslog clássico + polite/smtp"},
	}

	for _, tc := range lines {
		t.Run(tc.desc, func(t *testing.T) {
			// O teste apenas verifica que a linha não causa panic e é processada
			// (seja como métrica reconhecida ou como unsupported — nunca como crash)
			assert.NotPanics(t, func() {
				e.CollectFromLogLine(tc.line)
			})
		})
	}

	// Total de entregas smtp: deferred + sent + bounced (polite/smtp)
	// + deferred (ultramegaturtle/smtp) + sent (syslog clássico/polite/smtp) = 5
	// A linha postfix/discard não é mapeada para smtp, então não conta.
	assert.Equal(t, 5.0, counterVecTotal(t, e.smtpProcesses),
		"subprocessos aninhados (/polite/smtp, /ultramegaturtle/smtp) devem ser tratados como smtp")
}

// TestOldTimestampsAreIgnored confirma explicitamente que o exporter
// processa linhas com timestamps de anos passados da mesma forma que
// linhas com timestamps atuais.
func TestOldTimestampsAreIgnored(t *testing.T) {
	e := newTestExporter(t)

	// Mesma mensagem com timestamps de anos diferentes
	lines := []string{
		// Timestamp de 2020
		"Jan  1 00:00:01 mx1 postfix/cleanup[1]: ABC: message-id=<a@b.com>",
		// Timestamp de 2019
		"Dec 31 23:59:59 mx1 postfix/cleanup[2]: DEF: message-id=<c@d.com>",
		// Timestamp de 1999
		"Jan  1 00:00:00 mx1 postfix/cleanup[3]: GHI: message-id=<e@f.com>",
		// Sem timestamp (formato alternativo)
		"postfix/cleanup[4]: JKL: message-id=<g@h.com>",
	}

	for _, line := range lines {
		e.CollectFromLogLine(line)
	}

	// Todas as 4 linhas devem ter sido contadas, independente do timestamp
	assert.Equal(t, 4.0, counterValue(t, e.cleanupProcesses),
		"timestamps antigos devem ser ignorados — todas as 4 linhas devem ser contadas")
}
