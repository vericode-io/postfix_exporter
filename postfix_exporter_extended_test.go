package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// counterValue lê o valor de um prometheus.Counter.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	close(ch)
	m := <-ch
	dto := &io_prometheus_client.Metric{}
	require.NoError(t, m.Write(dto))
	return dto.Counter.GetValue()
}

// counterVecTotal soma todos os valores de um *prometheus.CounterVec.
func counterVecTotal(t *testing.T, cv *prometheus.CounterVec) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 16)
	go func() {
		cv.Collect(ch)
		close(ch)
	}()
	var total float64
	for m := range ch {
		dto := &io_prometheus_client.Metric{}
		require.NoError(t, m.Write(dto))
		total += dto.Counter.GetValue()
	}
	return total
}

// newTestExporter cria um PostfixExporter mínimo para testes de log lines.
func newTestExporter(t *testing.T) *PostfixExporter {
	t.Helper()
	e, err := NewPostfixExporter("", nil, true)
	require.NoError(t, err)
	return e
}

// ---------------------------------------------------------------------------
// Testes de NewPostfixExporter
// ---------------------------------------------------------------------------

func TestNewPostfixExporter_ReturnsNoError(t *testing.T) {
	e, err := NewPostfixExporter("/var/spool/postfix/public/showq", nil, false)
	require.NoError(t, err)
	assert.NotNil(t, e)
}

func TestNewPostfixExporter_WithLogUnsupported(t *testing.T) {
	e, err := NewPostfixExporter("", nil, true)
	require.NoError(t, err)
	assert.True(t, e.logUnsupportedLines)
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — cleanup
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_CleanupMessageID(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/cleanup[1234]: AABBCC: message-id=<abc@example.com>")
	assert.Equal(t, 1.0, counterValue(t, e.cleanupProcesses))
}

func TestCollectFromLogLine_CleanupReject(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/cleanup[1234]: AABBCC: reject: header From: bad@example.com")
	assert.Equal(t, 1.0, counterValue(t, e.cleanupRejects))
}

func TestCollectFromLogLine_CleanupUnsupported(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/cleanup[1234]: AABBCC: something unknown here")
	assert.Equal(t, 1.0, counterVecTotal(t, e.unsupportedLogEntries))
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — qmgr
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_QmgrInsert(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/qmgr[1234]: AABBCC: from=<sender@example.com>, size=12345, nrcpt=2 (queue active)")
	// Verifica que os histogramas foram observados (não há erro de nil pointer)
	ch := make(chan prometheus.Metric, 32)
	e.qmgrInsertsSize.Collect(ch)
	e.qmgrInsertsNrcpt.Collect(ch)
	close(ch)
	count := 0
	for range ch {
		count++
	}
	assert.Greater(t, count, 0)
}

func TestCollectFromLogLine_QmgrRemoved(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/qmgr[1234]: AABBCC: removed")
	assert.Equal(t, 1.0, counterValue(t, e.qmgrRemoves))
}

func TestCollectFromLogLine_QmgrExpired(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/qmgr[1234]: AABBCC: from=<x@example.com>, status=expired, returned to sender")
	assert.Equal(t, 1.0, counterValue(t, e.qmgrExpires))
}

func TestCollectFromLogLine_QmgrForceExpired(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/qmgr[1234]: AABBCC: from=<x@example.com>, status=force-expired, returned to sender")
	assert.Equal(t, 1.0, counterValue(t, e.qmgrExpires))
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — smtp (outgoing)
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_SmtpSent(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtp[1234]: AABBCC: to=<rcpt@example.com>, relay=mx.example.com[1.2.3.4]:25, delay=1.0, delays=0.1/0.2/0.3/0.4, dsn=2.0.0, status=sent (250 OK)")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpProcesses))
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpProcessesByDSN))
}

func TestCollectFromLogLine_SmtpDeferred(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtp[1234]: AABBCC: to=<rcpt@example.com>, relay=mx.example.com[1.2.3.4]:25, delay=1.0, delays=0.1/0.2/0.3/0.4, dsn=4.0.0, status=deferred (connection refused)")
	assert.Equal(t, 1.0, counterValue(t, e.smtpStatusDeferred))
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpProcesses))
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpProcessesByDSN))
}

func TestCollectFromLogLine_SmtpConnectionTimedOut(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtp[1234]: connect to mx.example.com[1.2.3.4]:25: Connection timed out")
	assert.Equal(t, 1.0, counterValue(t, e.smtpConnectionTimedOut))
}

func TestCollectFromLogLine_SmtpConnectionReset_RcptTo(t *testing.T) {
	e := newTestExporter(t)
	// Remote server (Outlook/Hotmail) actively dropped the connection during RCPT TO
	e.CollectFromLogLine("2026-06-14T13:37:31.082923-03:00 mx3 postfix/megaturtle/smtp[94965]: 4gddkg5ncrz51v5: lost connection with outlook-com.olc.protection.OUTLOOK.COM[52.101.137.1] while sending RCPT TO")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpConnectionReset))
	assert.Equal(t, 0.0, counterValue(t, e.smtpConnectionTimedOut), "should NOT count as timed out")
}

func TestCollectFromLogLine_SmtpConnectionReset_Data(t *testing.T) {
	e := newTestExporter(t)
	// Remote server dropped connection while sending DATA
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtp[1234]: AABBCC: lost connection with mx.example.com[1.2.3.4] while sending DATA")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpConnectionReset))
}

func TestCollectFromLogLine_SmtpConnectionReset_Greeting(t *testing.T) {
	e := newTestExporter(t)
	// Remote server accepted TCP but stalled before sending SMTP banner
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtp[1234]: AABBCC: lost connection with mx.example.com[1.2.3.4] while receiving the initial server greeting")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpConnectionReset))
	assert.Equal(t, 0.0, counterValue(t, e.smtpConnectionTimedOut), "greeting reset should NOT count as timed out")
}

func TestCollectFromLogLine_SmtpConnectionTimedOut_Conversation(t *testing.T) {
	e := newTestExporter(t)
	// Conversation timed out (different from active reset — no 'lost connection with')
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtp[1234]: AABBCC: conversation with mx.huawei.com[1.2.3.4] timed out while receiving the initial server greeting")
	assert.Equal(t, 1.0, counterValue(t, e.smtpConnectionTimedOut))
	assert.Equal(t, 0.0, counterVecTotal(t, e.smtpConnectionReset), "conversation timeout should NOT count as reset")
}

func TestCollectFromLogLine_SmtpTLSOutgoing(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtp[1234]: Verified TLS connection established to mx.example.com[1.2.3.4]:25: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits)")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpTLSConnects))
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — smtpd (incoming)
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_SmtpTLSHandshakeFailure(t *testing.T) {
	e := newTestExporter(t)
	// Real production line: TLS handshake failed on nested subprocess (polite/smtp)
	e.CollectFromLogLine("2026-06-16T17:18:31.925885-03:00 PostfixCSUPorto postfix/polite/smtp[35821]: 4gfyry6Qhcz4ydk: Cannot start TLS: handshake failure")
	assert.Equal(t, 1.0, counterValue(t, e.smtpTLSHandshakeFailures), "should count TLS handshake failure")
	assert.Equal(t, 0.0, counterValue(t, e.smtpConnectionTimedOut), "should NOT count as connection timed out")
	assert.Equal(t, 0.0, counterVecTotal(t, e.smtpConnectionReset), "should NOT count as connection reset")
	assert.Equal(t, 0.0, counterVecTotal(t, e.unsupportedLogEntries), "should NOT be unsupported")
}

func TestCollectFromLogLine_SmtpTLSHandshakeFailure_Classic(t *testing.T) {
	e := newTestExporter(t)
	// Classic syslog timestamp format
	e.CollectFromLogLine("Jun 16 17:18:31 mail postfix/smtp[35821]: AABBCC: Cannot start TLS: handshake failure")
	assert.Equal(t, 1.0, counterValue(t, e.smtpTLSHandshakeFailures))
	assert.Equal(t, 0.0, counterVecTotal(t, e.unsupportedLogEntries), "should NOT be unsupported")
}

func TestCollectFromLogLine_SmtpdConnect(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: connect from client.example.com[1.2.3.4]")
	assert.Equal(t, 1.0, counterValue(t, e.smtpdConnects))
}

func TestCollectFromLogLine_SmtpdDisconnect(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: disconnect from client.example.com[1.2.3.4]")
	assert.Equal(t, 1.0, counterValue(t, e.smtpdDisconnects))
}

func TestCollectFromLogLine_SmtpdFCrDNSError(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: warning: hostname bad.host does not resolve to address 1.2.3.4")
	assert.Equal(t, 1.0, counterValue(t, e.smtpdFCrDNSErrors))
}

func TestCollectFromLogLine_SmtpdLostConnection(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: lost connection after AUTH from client.example.com[1.2.3.4]")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpdLostConnections))
}

func TestCollectFromLogLine_SmtpdSASLAuthFailed(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: warning: client.example.com[1.2.3.4]: SASL PLAIN authentication failed: generic failure")
	assert.Equal(t, 1.0, counterValue(t, e.smtpdSASLAuthenticationFailures))
}

func TestCollectFromLogLine_SmtpdReject(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: NOQUEUE: reject: RCPT from client.example.com[1.2.3.4]: 550 5.1.1 <rcpt@example.com>: Recipient address rejected")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpdRejects))
}

func TestCollectFromLogLine_SmtpdTLSIncoming(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: Untrusted TLS connection established from client.example.com[1.2.3.4]: TLSv1.2 with cipher ECDHE-RSA-AES128-GCM-SHA256 (128/128 bits)")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpdTLSConnects))
}

func TestCollectFromLogLine_SmtpdClientWithSASL(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: AABBCC: client=client.example.com[1.2.3.4], sasl_method=PLAIN, sasl_username=user@example.com")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpdProcesses))
}

func TestCollectFromLogLine_SmtpdClientWithoutSASL(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/smtpd[1234]: AABBCC: client=client.example.com[1.2.3.4]")
	assert.Equal(t, 1.0, counterVecTotal(t, e.smtpdProcesses))
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — lmtp
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_LmtpDelivery(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/lmtp[1234]: AABBCC: to=<rcpt@example.com>, relay=dovecot[/var/run/dovecot/lmtp], delay=0.5, delays=0.1/0.1/0.1/0.2, dsn=2.0.0, status=sent (250 OK)")
	ch := make(chan prometheus.Metric, 32)
	e.lmtpDelays.Collect(ch)
	close(ch)
	count := 0
	for range ch {
		count++
	}
	assert.Greater(t, count, 0)
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — pipe
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_PipeDelivery(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/pipe[1234]: AABBCC: to=<rcpt@example.com>, relay=spamassassin, delay=0.5, delays=0.1/0.1/0.1/0.2, dsn=2.0.0, status=sent (OK)")
	ch := make(chan prometheus.Metric, 32)
	e.pipeDelays.Collect(ch)
	close(ch)
	count := 0
	for range ch {
		count++
	}
	assert.Greater(t, count, 0)
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — bounce
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_BounceNonDelivery(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/bounce[1234]: AABBCC: sender non-delivery notification: DDEEFF")
	assert.Equal(t, 1.0, counterValue(t, e.bounceNonDelivery))
}

func TestCollectFromLogLine_BounceUnsupported(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/bounce[1234]: AABBCC: something else entirely")
	assert.Equal(t, 1.0, counterVecTotal(t, e.unsupportedLogEntries))
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — virtual
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_VirtualDelivered(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/virtual[1234]: AABBCC: to=<rcpt@example.com>, relay=virtual, delay=0.1, delays=0.1/0/0/0, dsn=2.0.0, status=sent (delivered to maildir)")
	assert.Equal(t, 1.0, counterValue(t, e.virtualDelivered))
}

func TestCollectFromLogLine_VirtualUnsupported(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/virtual[1234]: AABBCC: status=sent (delivered to mailbox)")
	assert.Equal(t, 1.0, counterVecTotal(t, e.unsupportedLogEntries))
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — opendkim
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_OpendkimSignatureAdded(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail opendkim[1234]: AABBCC: DKIM-Signature field added (s=default, d=example.com)")
	assert.Equal(t, 1.0, counterVecTotal(t, e.opendkimSignatureAdded))
}

func TestCollectFromLogLine_OpendkimUnsupported(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail opendkim[1234]: AABBCC: some other opendkim message")
	assert.Equal(t, 1.0, counterVecTotal(t, e.unsupportedLogEntries))
}

// ---------------------------------------------------------------------------
// Testes de CollectFromLogLine — entradas desconhecidas
// ---------------------------------------------------------------------------

func TestCollectFromLogLine_UnknownProcess(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("Jun 23 10:00:00 mail postfix/unknown_service[1234]: AABBCC: some message")
	assert.Equal(t, 1.0, counterVecTotal(t, e.unsupportedLogEntries))
}

func TestCollectFromLogLine_NoMatch(t *testing.T) {
	e := newTestExporter(t)
	e.CollectFromLogLine("this line does not match any known format at all")
	assert.Equal(t, 1.0, counterVecTotal(t, e.unsupportedLogEntries))
}

// ---------------------------------------------------------------------------
// Testes de ScanNullTerminatedEntries
// ---------------------------------------------------------------------------

func TestScanNullTerminatedEntries_ValidEntry(t *testing.T) {
	data := []byte("hello\x00world")
	advance, token, err := ScanNullTerminatedEntries(data, false)
	require.NoError(t, err)
	assert.Equal(t, 6, advance)
	assert.Equal(t, []byte("hello"), token)
}

func TestScanNullTerminatedEntries_NullAtStart(t *testing.T) {
	data := []byte("\x00rest")
	advance, token, err := ScanNullTerminatedEntries(data, false)
	require.NoError(t, err)
	assert.Equal(t, 1, advance)
	assert.Equal(t, []byte(""), token)
}

func TestScanNullTerminatedEntries_NoNullNotEOF(t *testing.T) {
	data := []byte("hello")
	advance, token, err := ScanNullTerminatedEntries(data, false)
	require.NoError(t, err)
	assert.Equal(t, 0, advance)
	assert.Nil(t, token)
}

func TestScanNullTerminatedEntries_NoNullAtEOF(t *testing.T) {
	data := []byte("hello")
	advance, token, err := ScanNullTerminatedEntries(data, true)
	assert.Error(t, err)
	assert.Equal(t, 0, advance)
	assert.Nil(t, token)
}

func TestScanNullTerminatedEntries_EmptyAtEOF(t *testing.T) {
	data := []byte{}
	advance, token, err := ScanNullTerminatedEntries(data, true)
	require.NoError(t, err)
	assert.Equal(t, 0, advance)
	assert.Nil(t, token)
}

// ---------------------------------------------------------------------------
// Testes de CollectShowqFromReader — auto-detecção
// ---------------------------------------------------------------------------

func TestCollectShowqFromReader_TextualFormat(t *testing.T) {
	input := `
-Queue ID-  --Size-- ----Arrival Time---- -Sender/Recipient-------
A07A81514      5156 Tue Feb 14 13:13:54  MAILER-DAEMON
                                         rcpt@example.com

`
	ch := make(chan prometheus.Metric, 32)
	err := CollectShowqFromReader(strings.NewReader(input), ch)
	close(ch)
	assert.NoError(t, err)
}

func TestCollectShowqFromReader_EmptyInput(t *testing.T) {
	ch := make(chan prometheus.Metric, 32)
	err := CollectShowqFromReader(strings.NewReader(""), ch)
	close(ch)
	assert.NoError(t, err)
}

func TestCollectShowqFromReader_BinaryFormat(t *testing.T) {
	// Formato binário do Postfix 3.x: pares chave\0valor\0 com \0\0 como separador de registro
	var buf bytes.Buffer
	writeField := func(k, v string) {
		buf.WriteString(k)
		buf.WriteByte(0)
		buf.WriteString(v)
		buf.WriteByte(0)
	}
	// Registro 1
	writeField("queue_name", "active")
	writeField("size", "1024")
	writeField("time", "1000000000")
	// Separador de registro: chave vazia
	buf.WriteByte(0)

	ch := make(chan prometheus.Metric, 32)
	err := CollectBinaryShowqFromReader(&buf, ch)
	close(ch)
	assert.NoError(t, err)
	// Deve ter coletado métricas
	count := 0
	for range ch {
		count++
	}
	assert.Greater(t, count, 0)
}

func TestCollectBinaryShowqFromReader_InvalidSize(t *testing.T) {
	var buf bytes.Buffer
	writeField := func(k, v string) {
		buf.WriteString(k)
		buf.WriteByte(0)
		buf.WriteString(v)
		buf.WriteByte(0)
	}
	writeField("queue_name", "active")
	writeField("size", "not_a_number")
	buf.WriteByte(0)

	ch := make(chan prometheus.Metric, 32)
	err := CollectBinaryShowqFromReader(&buf, ch)
	close(ch)
	assert.Error(t, err)
}

func TestCollectBinaryShowqFromReader_InvalidTime(t *testing.T) {
	var buf bytes.Buffer
	writeField := func(k, v string) {
		buf.WriteString(k)
		buf.WriteByte(0)
		buf.WriteString(v)
		buf.WriteByte(0)
	}
	writeField("queue_name", "deferred")
	writeField("time", "not_a_timestamp")
	buf.WriteByte(0)

	ch := make(chan prometheus.Metric, 32)
	err := CollectBinaryShowqFromReader(&buf, ch)
	close(ch)
	assert.Error(t, err)
}

func TestCollectBinaryShowqFromReader_MissingValue(t *testing.T) {
	// Chave sem valor correspondente (EOF abrupto)
	var buf bytes.Buffer
	buf.WriteString("queue_name")
	buf.WriteByte(0)
	// Sem valor — o scanner vai atingir EOF sem encontrar o valor

	ch := make(chan prometheus.Metric, 32)
	err := CollectBinaryShowqFromReader(&buf, ch)
	close(ch)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Testes de CollectTextualShowqFromReader
// ---------------------------------------------------------------------------

func TestCollectTextualShowqFromReader_ValidInput(t *testing.T) {
	input := `
A07A81514      5156 Tue Feb 14 13:13:54  MAILER-DAEMON
B07A81514*     2048 Tue Feb 14 13:14:00  sender@example.com
C07A81514!     1024 Tue Feb 14 13:15:00  hold@example.com
`
	ch := make(chan prometheus.Metric, 32)
	err := CollectTextualShowqFromReader(strings.NewReader(input), ch)
	close(ch)
	assert.NoError(t, err)
	count := 0
	for range ch {
		count++
	}
	assert.Greater(t, count, 0)
}

func TestCollectTextualShowqFromReader_InvalidSize(t *testing.T) {
	// Linha com tamanho inválido — o regex não vai capturar porque o tamanho deve ser \d+
	// Portanto, essa linha será ignorada silenciosamente (sem erro)
	input := "A07A81514      XXXX Tue Feb 14 13:13:54  MAILER-DAEMON\n"
	ch := make(chan prometheus.Metric, 32)
	err := CollectTextualShowqFromReader(strings.NewReader(input), ch)
	close(ch)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Testes de Describe e Collect
// ---------------------------------------------------------------------------

func TestPostfixExporter_Describe_WithoutLogSrc(t *testing.T) {
	e, err := NewPostfixExporter("", nil, false)
	require.NoError(t, err)

	ch := make(chan *prometheus.Desc, 64)
	e.Describe(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}
	// Sem logSrc, apenas postfixUpDesc deve ser descrito
	assert.Equal(t, 1, count)
}

func TestPostfixExporter_Describe_WithLogSrc(t *testing.T) {
	e, err := NewPostfixExporter("", &fakeLogSource{}, false)
	require.NoError(t, err)

	ch := make(chan *prometheus.Desc, 128)
	e.Describe(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}
	// Com logSrc, deve descrever todas as métricas
	assert.Greater(t, count, 1)
}

// ---------------------------------------------------------------------------
// Testes de StartMetricCollection
// ---------------------------------------------------------------------------

func TestStartMetricCollection_NilLogSrc(t *testing.T) {
	e, err := NewPostfixExporter("", nil, false)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancela imediatamente

	// Não deve bloquear nem entrar em pânico
	e.StartMetricCollection(ctx)
}

func TestStartMetricCollection_WithLines(t *testing.T) {
	lines := []string{
		"Jun 23 10:00:00 mail postfix/qmgr[1234]: AABBCC: removed",
		"Jun 23 10:00:01 mail postfix/qmgr[1234]: DDEEFF: removed",
	}
	src := &fakeLogSource{lines: lines}

	e, err := NewPostfixExporter("", src, false)
	require.NoError(t, err)

	ctx := context.Background()
	e.StartMetricCollection(ctx) // retorna quando src retorna io.EOF

	assert.Equal(t, 2.0, counterValue(t, e.qmgrRemoves))
}

// ---------------------------------------------------------------------------
// fakeLogSource para testes
// ---------------------------------------------------------------------------

type fakeLogSource struct {
	lines []string
	pos   int
}

func (f *fakeLogSource) Path() string { return "fake" }

func (f *fakeLogSource) Read(_ context.Context) (string, error) {
	if f.lines == nil || f.pos >= len(f.lines) {
		return "", io.EOF
	}
	line := f.lines[f.pos]
	f.pos++
	return line, nil
}
