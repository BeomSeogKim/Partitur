package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"
)

const (
	CodeParseError        = -32700
	CodeInvalidRequest    = -32600
	CodeMethodNotFound    = -32601
	CodeInvalidParams     = -32602
	CodeInternalError     = -32603
	CodeExecuteInProgress = -32000
	CodeFrameTooLarge     = -32001
)

var (
	ErrFrameTooLarge  = errors.New("JSON-RPC frame exceeds size limit")
	ErrPartialFrame   = errors.New("JSON-RPC stream ended with a partial frame")
	ErrParseRequest   = errors.New("JSON-RPC parse error")
	ErrInvalidRequest = errors.New("invalid JSON-RPC request")
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

func NewErrorResponse(id json.RawMessage, code int, message string) Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message}}
}

func NewResultResponse(id json.RawMessage, result any) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

func DecodeRequest(line []byte) (Request, error) {
	if !utf8.Valid(line) {
		return Request{}, ErrParseRequest
	}

	var request Request
	if err := DecodeStrict(line, &request); err != nil {
		var syntaxError *json.SyntaxError
		if errors.As(err, &syntaxError) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Request{}, fmt.Errorf("%w: %v", ErrParseRequest, err)
		}
		return Request{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if request.JSONRPC != "2.0" || request.Method == "" || !validRequestID(request.ID) {
		return request, ErrInvalidRequest
	}
	return request, nil
}

// DecodeStrict decodes exactly one valid UTF-8 JSON value and rejects unknown
// fields and duplicate object keys.
func DecodeStrict(data []byte, value any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return rejectDuplicateJSONKeys(data)
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			keys[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func validRequestID(id json.RawMessage) bool {
	if len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		return false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

// Writer serializes JSON-RPC responses and notifications onto one JSONL stream.
type Writer struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: bufio.NewWriter(w)}
}

func (w *Writer) WriteResponse(response Response) error {
	return w.write(response)
}

func (w *Writer) WriteNotification(method string, params any) error {
	return w.write(Notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (w *Writer) write(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal JSON-RPC message: %w", err)
	}
	if len(encoded) > MaxFrameBytes {
		return ErrFrameTooLarge
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(encoded); err != nil {
		return fmt.Errorf("write JSON-RPC response: %w", err)
	}
	if err := w.w.WriteByte('\n'); err != nil {
		return fmt.Errorf("write JSON-RPC newline: %w", err)
	}
	return w.w.Flush()
}

// Reader reads bounded JSONL control frames from the core.
type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r)}
}

// ReadFrame skips blank lines. An oversized line is consumed through its
// newline so the caller may report the error and continue.
func (r *Reader) ReadFrame() ([]byte, error) {
	for {
		line, err := r.readLine(MaxFrameBytes)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) != 0 {
			return line, nil
		}
	}
}

func (r *Reader) readLine(limit int) ([]byte, error) {
	var line []byte
	tooLarge := false

	for {
		b, err := r.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && (len(line) > 0 || tooLarge) {
				return nil, ErrPartialFrame
			}
			return nil, err
		}
		if b == '\n' {
			if tooLarge {
				return nil, ErrFrameTooLarge
			}
			return line, nil
		}
		if !tooLarge {
			if len(line) >= limit {
				tooLarge = true
				continue
			}
			line = append(line, b)
		}
	}
}
