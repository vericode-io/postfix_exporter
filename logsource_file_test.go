package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileLogSource_Path(t *testing.T) {
	path, close, err := setupFakeLogFile()
	if err != nil {
		t.Fatalf("setupFakeTailer failed: %v", err)
	}
	defer close()

	src, err := NewFileLogSource(path)
	if err != nil {
		t.Fatalf("NewFileLogSource failed: %v", err)
	}
	defer src.Close()

	assert.Equal(t, path, src.Path(), "Path should be set by New.")
}

func TestFileLogSource_Read(t *testing.T) {
	ctx := context.Background()

	path, close, err := setupFakeLogFile()
	if err != nil {
		t.Fatalf("setupFakeTailer failed: %v", err)
	}
	defer close()

	src, err := NewFileLogSource(path)
	if err != nil {
		t.Fatalf("NewFileLogSource failed: %v", err)
	}
	defer src.Close()

	s, err := src.Read(ctx)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	assert.Equal(t, "Feb 13 23:31:30 ahost anid[123]: aline", s, "Read should get data from the journal entry.")
}

// TestNewFileLogSource_FileNotFound verifica que um erro é retornado quando
// o arquivo de log não existe (MustExist: true).
func TestNewFileLogSource_FileNotFound(t *testing.T) {
	_, err := NewFileLogSource("/tmp/this_file_does_not_exist_postfix_test_xyz.log")
	assert.Error(t, err, "deve retornar erro para arquivo inexistente")
}

// TestFileLogSource_Read_ContextCancelled verifica que Read respeita o
// cancelamento de contexto quando não há linhas disponíveis.
func TestFileLogSource_Read_ContextCancelled(t *testing.T) {
	path, closeFn, err := setupFakeLogFile()
	require.NoError(t, err)
	defer closeFn()

	src, err := NewFileLogSource(path)
	require.NoError(t, err)
	defer src.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancela imediatamente

	_, err = src.Read(ctx)
	assert.Equal(t, context.Canceled, err)
}

// TestFileLogSourceFactory_New_EmptyPath verifica que a factory retorna
// nil, nil quando o path está vazio (log source desabilitado).
func TestFileLogSourceFactory_New_EmptyPath(t *testing.T) {
	factory := &fileLogSourceFactory{path: ""}
	src, err := factory.New(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, src)
}

// TestFileLogSourceFactory_New_ValidPath verifica que a factory cria um
// FileLogSource quando o path aponta para um arquivo existente.
func TestFileLogSourceFactory_New_ValidPath(t *testing.T) {
	path, closeFn, err := setupFakeLogFile()
	require.NoError(t, err)
	defer closeFn()

	factory := &fileLogSourceFactory{path: path}
	src, err := factory.New(context.Background())
	require.NoError(t, err)
	require.NotNil(t, src)
	src.Close()
}

// TestRegisterLogSourceFactory verifica que RegisterLogSourceFactory
// adiciona a factory à lista global.
func TestRegisterLogSourceFactory(t *testing.T) {
	original := logSourceFactories
	defer func() { logSourceFactories = original }()

	logSourceFactories = nil
	factory := &fileLogSourceFactory{path: ""}
	RegisterLogSourceFactory(factory)
	assert.Len(t, logSourceFactories, 1)
}

// TestNewLogSourceFromFactories_NoSource verifica que um erro é retornado
// quando nenhuma factory consegue criar um log source.
func TestNewLogSourceFromFactories_NoSource(t *testing.T) {
	original := logSourceFactories
	defer func() { logSourceFactories = original }()

	logSourceFactories = []LogSourceFactory{&fileLogSourceFactory{path: ""}}

	_, err := NewLogSourceFromFactories(context.Background())
	assert.Error(t, err, "deve retornar erro quando nenhum source está configurado")
	assert.Contains(t, err.Error(), "no log source configured")
}

// TestNewLogSourceFromFactories_WithSource verifica que a factory retorna
// um source válido quando o arquivo existe.
func TestNewLogSourceFromFactories_WithSource(t *testing.T) {
	original := logSourceFactories
	defer func() { logSourceFactories = original }()

	path, closeFn, err := setupFakeLogFile()
	require.NoError(t, err)
	defer closeFn()

	logSourceFactories = []LogSourceFactory{&fileLogSourceFactory{path: path}}

	src, err := NewLogSourceFromFactories(context.Background())
	require.NoError(t, err)
	require.NotNil(t, src)
	src.Close()
}

func setupFakeLogFile() (string, func(), error) {
	f, err := ioutil.TempFile("", "filelogsource")
	if err != nil {
		return "", nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer os.Remove(f.Name())
		defer f.Close()

		for {
			// The tailer seeks to the end and then does a
			// follow. Keep writing lines so we know it wakes up and
			// returns lines.
			fmt.Fprintln(f, "Feb 13 23:31:30 ahost anid[123]: aline")

			select {
			case <-time.After(10 * time.Millisecond):
				// continue
			case <-ctx.Done():
				return
			}
		}
	}()

	return f.Name(), func() {
		cancel()
		wg.Wait()
	}, nil
}
