package httpx

import "sync"

type BufferPool struct {
	pool sync.Pool
}

func NewBufferPool(size int) *BufferPool {
	return &BufferPool{
		pool: sync.Pool{
			New: func() any {
				buf := make([]byte, size)
				return &buf
			},
		},
	}
}

func (p *BufferPool) Get() []byte {
	if p == nil {
		return nil
	}
	buf := p.pool.Get().(*[]byte)
	return (*buf)[:cap(*buf)]
}

func (p *BufferPool) Put(buf []byte) {
	if p == nil || cap(buf) == 0 {
		return
	}
	buf = buf[:cap(buf)]
	p.pool.Put(&buf)
}
