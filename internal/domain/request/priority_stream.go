package request

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"sync"
)

const extraBodyField = "extra_body"
const messagesField = "messages"
const toolCallsField = "tool_calls"
const priorityStreamBufferSize = 2 * 1024 * 1024
const priorityStreamMinBufferSize = 4 * 1024

var quotedExtraBodyField = []byte(strconv.Quote(extraBodyField))
var quotedMessagesField = []byte(strconv.Quote(messagesField))
var quotedToolCallsField = []byte(strconv.Quote(toolCallsField))

var priorityStreamPools sync.Map

type priorityStreamPool struct {
	readers sync.Pool
	writers sync.Pool
	buffers sync.Pool
}

type priorityObjectKey int

const (
	priorityObjectKeyOther priorityObjectKey = iota
	priorityObjectKeyField
	priorityObjectKeyExtraBody
	priorityObjectKeyMessages
	priorityObjectKeyToolCalls
)

type priorityRewriteMode int

const (
	priorityRewriteNone priorityRewriteMode = iota
	priorityRewriteFieldScan
	priorityRewriteAppendLast
)

type streamingPriorityReadCloser struct {
	reader *io.PipeReader
	source io.Closer
	once   sync.Once
	err    error
}

func NewStreamingJSONPriorityRewrite(source io.ReadCloser, field string, value int) (io.ReadCloser, error) {
	return NewStreamingJSONPriorityRewriteSize(source, field, value, priorityStreamBufferSize)
}

func NewStreamingJSONPriorityRewriteSize(source io.ReadCloser, field string, value int, bufferSize int) (io.ReadCloser, error) {
	return NewStreamingJSONBodyRewriteSize(source, JSONRewriteOptions{
		InjectPriority:   true,
		PriorityStrategy: PriorityRewriteStrategyFieldScan,
		PriorityField:    field,
		PriorityValue:    value,
	}, bufferSize)
}

func NewStreamingJSONBodyRewriteSize(source io.ReadCloser, options JSONRewriteOptions, bufferSize int) (io.ReadCloser, error) {
	if options.InjectPriority && options.PriorityField == "" {
		return nil, errors.New("priority field must not be empty")
	}
	bufferSize = normalizedPriorityStreamBufferSize(bufferSize)
	reader, writer := io.Pipe()
	stream := &streamingPriorityReadCloser{reader: reader, source: source}
	go func() {
		bufferedReader := acquirePriorityReader(source, bufferSize)
		err := rewriteJSONBodyStreamWithBuffer(bufferedReader, writer, options, bufferSize)
		releasePriorityReader(bufferedReader, bufferSize)
		_ = source.Close()
		_ = writer.CloseWithError(err)
	}()
	return stream, nil
}

func (s *streamingPriorityReadCloser) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *streamingPriorityReadCloser) Close() error {
	s.once.Do(func() {
		readerErr := s.reader.Close()
		sourceErr := s.source.Close()
		if readerErr != nil {
			s.err = readerErr
		} else {
			s.err = sourceErr
		}
	})
	return s.err
}

type priorityStreamRewriter struct {
	reader        *bufio.Reader
	writer        io.Writer
	field         string
	quotedField   []byte
	priority      string
	priorityField string
	priorityMode  priorityRewriteMode
	stripTools    bool
}

func rewriteJSONBodyStreamWithBuffer(reader *bufio.Reader, writer io.Writer, options JSONRewriteOptions, bufferSize int) error {
	bufferSize = normalizedPriorityStreamBufferSize(bufferSize)
	buffered := acquirePriorityWriter(writer, bufferSize)
	mode, err := rewriteMode(options)
	if err != nil {
		releasePriorityWriter(buffered, bufferSize)
		return err
	}
	quotedField := ""
	if options.PriorityField != "" {
		quotedField = strconv.Quote(options.PriorityField)
	}
	rewriter := priorityStreamRewriter{
		reader:        reader,
		writer:        buffered,
		field:         options.PriorityField,
		quotedField:   []byte(quotedField),
		priority:      strconv.Itoa(options.PriorityValue),
		priorityField: quotedField + ":" + strconv.Itoa(options.PriorityValue),
		priorityMode:  mode,
		stripTools:    options.StripEmptyToolCalls,
	}
	err = rewriter.rewriteTopObject()
	if flushErr := buffered.Flush(); err == nil {
		err = flushErr
	}
	releasePriorityWriter(buffered, bufferSize)
	return err
}

func rewriteMode(options JSONRewriteOptions) (priorityRewriteMode, error) {
	if !options.InjectPriority {
		return priorityRewriteNone, nil
	}
	if options.PriorityField == "" {
		return priorityRewriteNone, errors.New("priority field must not be empty")
	}
	switch options.PriorityStrategy {
	case "", PriorityRewriteStrategyFieldScan:
		return priorityRewriteFieldScan, nil
	case PriorityRewriteStrategyAppendLast:
		return priorityRewriteAppendLast, nil
	default:
		return priorityRewriteNone, errors.New("unknown priority rewrite strategy")
	}
}

func (r *priorityStreamRewriter) rewriteTopObject() error {
	first, err := r.readNonSpace()
	if err != nil {
		return err
	}
	if first != '{' {
		return ErrPriorityBodyNotObject
	}
	if err := r.writeByte('{'); err != nil {
		return err
	}
	wroteField := false
	if r.priorityMode == priorityRewriteFieldScan {
		if err := r.writePriorityField(&wroteField); err != nil {
			return err
		}
	}

	next, err := r.readNonSpace()
	if err != nil {
		return err
	}
	if next == '}' {
		if r.priorityMode == priorityRewriteAppendLast {
			if err := r.writePriorityField(&wroteField); err != nil {
				return err
			}
		}
		return r.writeByte('}')
	}
	if err := r.reader.UnreadByte(); err != nil {
		return err
	}

	for {
		keyLiteral, key, err := r.readObjectKey()
		if err != nil {
			return err
		}
		if err := r.expectColon(); err != nil {
			return err
		}
		valueStart, err := r.readNonSpace()
		if err != nil {
			return err
		}

		if r.priorityMode == priorityRewriteFieldScan && key == priorityObjectKeyField {
			if err := r.skipValue(valueStart); err != nil {
				return err
			}
		} else {
			if err := r.writeObjectFieldPrefix(&wroteField, keyLiteral); err != nil {
				return err
			}
			if r.priorityMode == priorityRewriteFieldScan && key == priorityObjectKeyExtraBody && valueStart == '{' {
				if err := r.rewriteExtraBodyObject(); err != nil {
					return err
				}
			} else if r.stripTools && key == priorityObjectKeyMessages && valueStart == '[' {
				if err := r.rewriteMessagesArray(); err != nil {
					return err
				}
			} else if err := r.copyValue(valueStart); err != nil {
				return err
			}
		}

		delimiter, err := r.readNonSpace()
		if err != nil {
			return err
		}
		switch delimiter {
		case ',':
			continue
		case '}':
			if r.priorityMode == priorityRewriteAppendLast {
				if err := r.writePriorityField(&wroteField); err != nil {
					return err
				}
			}
			return r.writeByte('}')
		default:
			return errors.New("priority body object fields must be separated by comma")
		}
	}
}

func (r *priorityStreamRewriter) rewriteMessagesArray() error {
	if err := r.writeByte('['); err != nil {
		return err
	}
	next, err := r.readNonSpace()
	if err != nil {
		return err
	}
	if next == ']' {
		return r.writeByte(']')
	}
	if err := r.reader.UnreadByte(); err != nil {
		return err
	}

	wroteValue := false
	for {
		valueStart, err := r.readNonSpace()
		if err != nil {
			return err
		}
		if wroteValue {
			if err := r.writeByte(','); err != nil {
				return err
			}
		}
		if valueStart == '{' {
			if err := r.rewriteMessageObject(); err != nil {
				return err
			}
		} else if err := r.copyValue(valueStart); err != nil {
			return err
		}
		wroteValue = true

		delimiter, err := r.readNonSpace()
		if err != nil {
			return err
		}
		switch delimiter {
		case ',':
			continue
		case ']':
			return r.writeByte(']')
		default:
			return errors.New("messages array values must be separated by comma")
		}
	}
}

func (r *priorityStreamRewriter) rewriteMessageObject() error {
	if err := r.writeByte('{'); err != nil {
		return err
	}
	next, err := r.readNonSpace()
	if err != nil {
		return err
	}
	if next == '}' {
		return r.writeByte('}')
	}
	if err := r.reader.UnreadByte(); err != nil {
		return err
	}

	wroteField := false
	for {
		keyLiteral, key, err := r.readObjectKey()
		if err != nil {
			return err
		}
		if err := r.expectColon(); err != nil {
			return err
		}
		valueStart, err := r.readNonSpace()
		if err != nil {
			return err
		}

		if key == priorityObjectKeyToolCalls && valueStart == '[' {
			empty, err := r.emptyArrayAfterOpen()
			if err != nil {
				return err
			}
			if empty {
				delimiter, err := r.readNonSpace()
				if err != nil {
					return err
				}
				switch delimiter {
				case ',':
					continue
				case '}':
					return r.writeByte('}')
				default:
					return errors.New("message object fields must be separated by comma")
				}
			}
			if err := r.writeObjectFieldPrefix(&wroteField, keyLiteral); err != nil {
				return err
			}
			if err := r.writeByte('['); err != nil {
				return err
			}
			if err := r.copyCompositeTailTo(']', r.writer); err != nil {
				return err
			}
		} else {
			if err := r.writeObjectFieldPrefix(&wroteField, keyLiteral); err != nil {
				return err
			}
			if err := r.copyValue(valueStart); err != nil {
				return err
			}
		}

		delimiter, err := r.readNonSpace()
		if err != nil {
			return err
		}
		switch delimiter {
		case ',':
			continue
		case '}':
			return r.writeByte('}')
		default:
			return errors.New("message object fields must be separated by comma")
		}
	}
}

func (r *priorityStreamRewriter) emptyArrayAfterOpen() (bool, error) {
	next, err := r.readNonSpace()
	if err != nil {
		return false, err
	}
	if next == ']' {
		return true, nil
	}
	if err := r.reader.UnreadByte(); err != nil {
		return false, err
	}
	return false, nil
}

func (r *priorityStreamRewriter) rewriteExtraBodyObject() error {
	if err := r.writeByte('{'); err != nil {
		return err
	}
	next, err := r.readNonSpace()
	if err != nil {
		return err
	}
	if next == '}' {
		return r.writeByte('}')
	}
	if err := r.reader.UnreadByte(); err != nil {
		return err
	}

	wroteField := false
	for {
		keyLiteral, key, err := r.readObjectKey()
		if err != nil {
			return err
		}
		if err := r.expectColon(); err != nil {
			return err
		}
		valueStart, err := r.readNonSpace()
		if err != nil {
			return err
		}

		if wroteField {
			if err := r.writeByte(','); err != nil {
				return err
			}
		}
		if err := r.writeBytes(keyLiteral); err != nil {
			return err
		}
		if err := r.writeByte(':'); err != nil {
			return err
		}
		if key == priorityObjectKeyField {
			if err := r.writeString(r.priority); err != nil {
				return err
			}
			if err := r.skipValue(valueStart); err != nil {
				return err
			}
		} else if err := r.copyValue(valueStart); err != nil {
			return err
		}
		wroteField = true

		delimiter, err := r.readNonSpace()
		if err != nil {
			return err
		}
		switch delimiter {
		case ',':
			continue
		case '}':
			return r.writeByte('}')
		default:
			return errors.New("extra_body object fields must be separated by comma")
		}
	}
}

func (r *priorityStreamRewriter) readObjectKey() ([]byte, priorityObjectKey, error) {
	first, err := r.readNonSpace()
	if err != nil {
		return nil, priorityObjectKeyOther, err
	}
	if first != '"' {
		return nil, priorityObjectKeyOther, errors.New("priority body object must contain string keys")
	}
	keyLiteral, err := r.readStringLiteral(first)
	if err != nil {
		return nil, priorityObjectKeyOther, err
	}
	key, err := r.objectKeyKind(keyLiteral)
	return keyLiteral, key, err
}

func (r *priorityStreamRewriter) objectKeyKind(keyLiteral []byte) (priorityObjectKey, error) {
	if r.field != "" && bytes.Equal(keyLiteral, r.quotedField) {
		return priorityObjectKeyField, nil
	}
	if bytes.Equal(keyLiteral, quotedExtraBodyField) {
		return priorityObjectKeyExtraBody, nil
	}
	if bytes.Equal(keyLiteral, quotedMessagesField) {
		return priorityObjectKeyMessages, nil
	}
	if bytes.Equal(keyLiteral, quotedToolCallsField) {
		return priorityObjectKeyToolCalls, nil
	}
	if bytes.IndexByte(keyLiteral, '\\') < 0 {
		return priorityObjectKeyOther, nil
	}
	key, err := strconv.Unquote(string(keyLiteral))
	if err != nil {
		return priorityObjectKeyOther, err
	}
	switch key {
	case r.field:
		if r.field != "" {
			return priorityObjectKeyField, nil
		}
		return priorityObjectKeyOther, nil
	case extraBodyField:
		return priorityObjectKeyExtraBody, nil
	case messagesField:
		return priorityObjectKeyMessages, nil
	case toolCallsField:
		return priorityObjectKeyToolCalls, nil
	default:
		return priorityObjectKeyOther, nil
	}
}

func (r *priorityStreamRewriter) expectColon() error {
	next, err := r.readNonSpace()
	if err != nil {
		return err
	}
	if next != ':' {
		return errors.New("priority body object key must be followed by colon")
	}
	return nil
}

func (r *priorityStreamRewriter) copyValue(first byte) error {
	return r.copyValueTo(first, r.writer)
}

func (r *priorityStreamRewriter) skipValue(first byte) error {
	return r.copyValueTo(first, io.Discard)
}

func (r *priorityStreamRewriter) copyValueTo(first byte, writer io.Writer) error {
	switch first {
	case '"':
		return r.copyStringTo(first, writer)
	case '{':
		return r.copyCompositeTo(first, '}', writer)
	case '[':
		return r.copyCompositeTo(first, ']', writer)
	default:
		return r.copyScalarTo(first, writer)
	}
}

func (r *priorityStreamRewriter) copyStringTo(first byte, writer io.Writer) error {
	if err := writeByte(writer, first); err != nil {
		return err
	}
	return r.copyStringTailTo(writer)
}

func (r *priorityStreamRewriter) copyCompositeTo(first, close byte, writer io.Writer) error {
	if err := writeByte(writer, first); err != nil {
		return err
	}
	return r.copyCompositeTailTo(close, writer)
}

func (r *priorityStreamRewriter) copyCompositeTailTo(close byte, writer io.Writer) error {
	stack := []byte{close}
	inString := false
	escaped := false
	for {
		chunk, err := r.peekBuffered()
		if err != nil {
			return err
		}
		scan := 0
		for scan < len(chunk) {
			if inString {
				if escaped {
					escaped = false
					scan++
					continue
				}
				offset := bytes.IndexAny(chunk[scan:], "\\\"")
				if offset < 0 {
					scan = len(chunk)
					break
				}
				i := scan + offset
				switch chunk[i] {
				case '\\':
					escaped = true
				case '"':
					inString = false
				}
				scan = i + 1
				continue
			}

			offset := bytes.IndexAny(chunk[scan:], "\"{}[]")
			if offset < 0 {
				scan = len(chunk)
				break
			}
			i := scan + offset
			next := chunk[i]
			switch next {
			case '"':
				inString = true
				scan = i + 1
			case '{':
				stack = append(stack, '}')
				scan = i + 1
			case '[':
				stack = append(stack, ']')
				scan = i + 1
			case '}', ']':
				last := len(stack) - 1
				if last < 0 || next != stack[last] {
					return errors.New("priority body contains mismatched JSON delimiter")
				}
				stack = stack[:last]
				scan = i + 1
				if len(stack) == 0 {
					if _, writeErr := writer.Write(chunk[:scan]); writeErr != nil {
						return writeErr
					}
					_, discardErr := r.reader.Discard(scan)
					return discardErr
				}
			}
		}
		if scan > 0 {
			if _, writeErr := writer.Write(chunk[:scan]); writeErr != nil {
				return writeErr
			}
			if _, discardErr := r.reader.Discard(scan); discardErr != nil {
				return discardErr
			}
		}
	}
}

func (r *priorityStreamRewriter) writeObjectFieldPrefix(wroteField *bool, keyLiteral []byte) error {
	if *wroteField {
		if err := r.writeByte(','); err != nil {
			return err
		}
	}
	if err := r.writeBytes(keyLiteral); err != nil {
		return err
	}
	if err := r.writeByte(':'); err != nil {
		return err
	}
	*wroteField = true
	return nil
}

func (r *priorityStreamRewriter) writePriorityField(wroteField *bool) error {
	if r.priorityMode == priorityRewriteNone {
		return nil
	}
	if *wroteField {
		if err := r.writeByte(','); err != nil {
			return err
		}
	}
	if err := r.writeString(r.priorityField); err != nil {
		return err
	}
	*wroteField = true
	return nil
}

func (r *priorityStreamRewriter) peekBuffered() ([]byte, error) {
	if r.reader.Buffered() == 0 {
		if _, err := r.reader.Peek(1); err != nil {
			return nil, err
		}
	}
	return r.reader.Peek(r.reader.Buffered())
}

func (r *priorityStreamRewriter) copyStringTailTo(writer io.Writer) error {
	escaped := false
	for {
		chunk, err := r.peekBuffered()
		if err != nil {
			return err
		}
		scan := 0
		for scan < len(chunk) {
			if escaped {
				escaped = false
				scan++
				continue
			}
			offset := bytes.IndexAny(chunk[scan:], "\\\"")
			if offset < 0 {
				scan = len(chunk)
				break
			}
			i := scan + offset
			next := chunk[i]
			switch next {
			case '\\':
				escaped = true
				scan = i + 1
			case '"':
				scan = i + 1
				if _, writeErr := writer.Write(chunk[:scan]); writeErr != nil {
					return writeErr
				}
				_, discardErr := r.reader.Discard(scan)
				return discardErr
			}
		}
		if scan > 0 {
			if _, writeErr := writer.Write(chunk[:scan]); writeErr != nil {
				return writeErr
			}
			if _, discardErr := r.reader.Discard(scan); discardErr != nil {
				return discardErr
			}
		}
	}
}

func (r *priorityStreamRewriter) copyScalarTo(first byte, writer io.Writer) error {
	if err := writeByte(writer, first); err != nil {
		return err
	}
	for {
		next, err := r.reader.ReadByte()
		if err != nil {
			return err
		}
		switch next {
		case ',', '}', ']':
			return r.reader.UnreadByte()
		default:
			if err := writeByte(writer, next); err != nil {
				return err
			}
		}
	}
}

func (r *priorityStreamRewriter) readStringLiteral(first byte) ([]byte, error) {
	literal := make([]byte, 0, 32)
	literal = append(literal, first)
	for {
		next, err := r.reader.ReadByte()
		if err != nil {
			return nil, err
		}
		literal = append(literal, next)
		switch next {
		case '\\':
			escaped, err := r.reader.ReadByte()
			if err != nil {
				return nil, err
			}
			literal = append(literal, escaped)
		case '"':
			return literal, nil
		}
	}
}

func (r *priorityStreamRewriter) readNonSpace() (byte, error) {
	for {
		next, err := r.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch next {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			return next, nil
		}
	}
}

func (r *priorityStreamRewriter) writeByte(value byte) error {
	return writeByte(r.writer, value)
}

func (r *priorityStreamRewriter) writeBytes(value []byte) error {
	_, err := r.writer.Write(value)
	return err
}

func (r *priorityStreamRewriter) writeString(value string) error {
	_, err := io.WriteString(r.writer, value)
	return err
}

func writeByte(writer io.Writer, value byte) error {
	if byteWriter, ok := writer.(interface{ WriteByte(byte) error }); ok {
		return byteWriter.WriteByte(value)
	}
	var b [1]byte
	b[0] = value
	_, err := writer.Write(b[:])
	return err
}

func normalizedPriorityStreamBufferSize(bufferSize int) int {
	if bufferSize <= 0 {
		return priorityStreamBufferSize
	}
	if bufferSize < priorityStreamMinBufferSize {
		return priorityStreamMinBufferSize
	}
	return bufferSize
}

func priorityPool(bufferSize int) *priorityStreamPool {
	bufferSize = normalizedPriorityStreamBufferSize(bufferSize)
	if pool, ok := priorityStreamPools.Load(bufferSize); ok {
		return pool.(*priorityStreamPool)
	}
	pool := &priorityStreamPool{}
	actual, _ := priorityStreamPools.LoadOrStore(bufferSize, pool)
	return actual.(*priorityStreamPool)
}

func acquirePriorityReader(reader io.Reader, bufferSize int) *bufio.Reader {
	pool := priorityPool(bufferSize)
	if value := pool.readers.Get(); value != nil {
		buffered := value.(*bufio.Reader)
		buffered.Reset(reader)
		return buffered
	}
	return bufio.NewReaderSize(reader, normalizedPriorityStreamBufferSize(bufferSize))
}

func releasePriorityReader(reader *bufio.Reader, bufferSize int) {
	reader.Reset(bytes.NewReader(nil))
	priorityPool(bufferSize).readers.Put(reader)
}

func acquirePriorityWriter(writer io.Writer, bufferSize int) *bufio.Writer {
	pool := priorityPool(bufferSize)
	if value := pool.writers.Get(); value != nil {
		buffered := value.(*bufio.Writer)
		buffered.Reset(writer)
		return buffered
	}
	return bufio.NewWriterSize(writer, normalizedPriorityStreamBufferSize(bufferSize))
}

func releasePriorityWriter(writer *bufio.Writer, bufferSize int) {
	writer.Reset(io.Discard)
	priorityPool(bufferSize).writers.Put(writer)
}

func acquirePriorityScratch(bufferSize int) []byte {
	bufferSize = normalizedPriorityStreamBufferSize(bufferSize)
	pool := priorityPool(bufferSize)
	if value := pool.buffers.Get(); value != nil {
		buffer := value.([]byte)
		return buffer[:bufferSize]
	}
	return make([]byte, bufferSize)
}

func releasePriorityScratch(buffer []byte, bufferSize int) {
	bufferSize = normalizedPriorityStreamBufferSize(bufferSize)
	if cap(buffer) < bufferSize {
		return
	}
	priorityPool(bufferSize).buffers.Put(buffer[:bufferSize])
}
