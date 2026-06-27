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
