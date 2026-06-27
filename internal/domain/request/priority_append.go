package request

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"sync"
)

type appendLastPriorityReadCloser struct {
	reader *io.PipeReader
	source io.Closer
	once   sync.Once
	err    error
}

func NewAppendLastJSONPriorityRewrite(source io.ReadCloser, field string, value int) (io.ReadCloser, error) {
	return NewAppendLastJSONPriorityRewriteSize(source, field, value, priorityStreamBufferSize)
}

func NewAppendLastJSONPriorityRewriteSize(source io.ReadCloser, field string, value int, bufferSize int) (io.ReadCloser, error) {
	if field == "" {
		return nil, errors.New("priority field must not be empty")
	}
	bufferSize = normalizedPriorityStreamBufferSize(bufferSize)
	reader, writer := io.Pipe()
	stream := &appendLastPriorityReadCloser{reader: reader, source: source}
	go func() {
		bufferedReader := acquirePriorityReader(source, bufferSize)
		err := appendLastPriorityStreamWithBuffer(bufferedReader, writer, field, strconv.Itoa(value), bufferSize)
		releasePriorityReader(bufferedReader, bufferSize)
		_ = source.Close()
		_ = writer.CloseWithError(err)
	}()
	return stream, nil
}

func (s *appendLastPriorityReadCloser) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *appendLastPriorityReadCloser) Close() error {
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

func appendLastPriorityStreamWithBuffer(reader *bufio.Reader, writer io.Writer, field, priority string, bufferSize int) error {
	bufferSize = normalizedPriorityStreamBufferSize(bufferSize)
	buffered := acquirePriorityWriter(writer, bufferSize)
	err := writeAppendLastPriorityObject(reader, buffered, strconv.Quote(field), priority, bufferSize)
	if flushErr := buffered.Flush(); err == nil {
		err = flushErr
	}
	releasePriorityWriter(buffered, bufferSize)
	return err
}

func writeAppendLastPriorityObject(reader *bufio.Reader, writer io.Writer, quotedField, priority string, bufferSize int) error {
	spaces, first, err := readNonSpaceBytes(reader)
	if err != nil {
		return err
	}
	if first != '{' {
		return ErrPriorityBodyNotObject
	}
	if _, err := writer.Write(spaces); err != nil {
		return err
	}
	if err := writeByte(writer, first); err != nil {
		return err
	}

	spaces, next, err := readNonSpaceBytes(reader)
	if err != nil {
		return err
	}
	if next == '}' {
		if err := writeAppendLastPriorityField(writer, quotedField, priority, false); err != nil {
			return err
		}
		if err := writeByte(writer, '}'); err != nil {
			return err
		}
		_, err = io.Copy(writer, reader)
		return err
	}
	if _, err := writer.Write(spaces); err != nil {
		return err
	}
	if err := reader.UnreadByte(); err != nil {
		return err
	}

	return copyObjectWithAppendedPriority(reader, writer, quotedField, priority, bufferSize)
}

func copyObjectWithAppendedPriority(reader *bufio.Reader, writer io.Writer, quotedField, priority string, bufferSize int) error {
	depth := 1
	inString := false
	escaped := false
	buffer := acquirePriorityScratch(bufferSize)
	defer releasePriorityScratch(buffer, bufferSize)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			start := 0
			for i := 0; i < len(chunk); {
				if inString {
					if escaped {
						escaped = false
						i++
						continue
					}
					nextOffset := bytes.IndexAny(chunk[i:], "\\\"")
					if nextOffset < 0 {
						break
					}
					i += nextOffset
					next := chunk[i]
					if escaped {
						escaped = false
						i++
						continue
					}
					switch next {
					case '\\':
						escaped = true
					case '"':
						inString = false
					}
					i++
					continue
				}
				nextOffset := bytes.IndexAny(chunk[i:], "\"{}[]")
				if nextOffset < 0 {
					break
				}
				i += nextOffset
				next := chunk[i]
				switch next {
				case '"':
					inString = true
					i++
				case '{', '[':
					depth++
					i++
				case ']':
					depth--
					if depth < 1 {
						return errors.New("priority body contains mismatched JSON delimiter")
					}
					i++
				case '}':
					if depth == 1 {
						if _, writeErr := writer.Write(chunk[start:i]); writeErr != nil {
							return writeErr
						}
						if writeErr := writeAppendLastPriorityField(writer, quotedField, priority, true); writeErr != nil {
							return writeErr
						}
						if writeErr := writeByte(writer, '}'); writeErr != nil {
							return writeErr
						}
						if i+1 < len(chunk) {
							if _, writeErr := writer.Write(chunk[i+1:]); writeErr != nil {
								return writeErr
							}
						}
						_, copyErr := io.Copy(writer, reader)
						return copyErr
					}
					depth--
					i++
				}
			}
			if _, writeErr := writer.Write(chunk[start:]); writeErr != nil {
				return writeErr
			}
		}
		if err == io.EOF {
			return errors.New("priority body object is not closed")
		}
		if err != nil {
			return err
		}
	}
}

func writeAppendLastPriorityField(writer io.Writer, quotedField, priority string, hasFields bool) error {
	if hasFields {
		if err := writeByte(writer, ','); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, quotedField+":"+priority)
	return err
}

func readNonSpaceBytes(reader *bufio.Reader) ([]byte, byte, error) {
	spaces := make([]byte, 0, 8)
	for {
		next, err := reader.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		switch next {
		case ' ', '\n', '\r', '\t':
			spaces = append(spaces, next)
		default:
			return spaces, next, nil
		}
	}
}
