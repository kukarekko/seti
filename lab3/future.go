package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

type Future[T any] struct {
	mu        sync.Mutex
	done      bool
	doneCh    chan struct{}
	val       T
	err       error
	callbacks []func(T, error)
}

func newFuture[T any]() *Future[T] {
	return &Future[T]{doneCh: make(chan struct{})}
}

func (f *Future[T]) complete(v T, err error) {
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.val, f.err = v, err
	f.done = true
	cbs := f.callbacks
	f.callbacks = nil
	close(f.doneCh)
	f.mu.Unlock()

	//вызываем все коллбэки когда задача готова
	for _, cb := range cbs {
		cb(v, err)
	}
}

//подписываемся на завершение без блокировки
func (f *Future[T]) onComplete(cb func(T, error)) {
	f.mu.Lock()
	if f.done {
		v, err := f.val, f.err
		f.mu.Unlock()
		cb(v, err)
		return
	}
	f.callbacks = append(f.callbacks, cb)
	f.mu.Unlock()
}

//ожидание результата
func (f *Future[T]) Await() (T, error) {
	<-f.doneCh
	return f.val, f.err
}

//заглушка при ошибке
func (f *Future[T]) orElse(fn func(error) T) *Future[T] {
	r := newFuture[T]()
	f.onComplete(func(v T, err error) {
		if err != nil {
			v = fn(err)
		}
		r.complete(v, nil)
	})
	return r
}

//запускает функцию в фоне(в отдельной горутине)
func Async[T any](fn func() (T, error)) *Future[T] {
	f := newFuture[T]()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				var zero T
				f.complete(zero, fmt.Errorf("panic: %v", r))
			}
		}()
		v, err := fn()
		f.complete(v, err)
	}()
	return f
}

//преобразовываем тип после получения результата
func Then[T, U any](f *Future[T], fn func(T) (U, error)) *Future[U] {
	r := newFuture[U]()
	f.onComplete(func(v T, err error) {
		if err != nil {
			var zero U
			r.complete(zero, err)
			return
		}
		u, fnErr := fn(v)
		r.complete(u, fnErr)
	})
	return r
}

//сделай А, когда А готово — сделай Б (без Await)
func ThenCompose[T, U any](f *Future[T], fn func(T) *Future[U]) *Future[U] {
	r := newFuture[U]()
	f.onComplete(func(v T, err error) {
		if err != nil {
			var zero U
			r.complete(zero, err)
			return
		}
		next := fn(v)
		next.onComplete(func(u U, nextErr error) {
			r.complete(u, nextErr)
		})
	})
	return r
}

//берёт два независимых Future и ждёт пока оба закончатся через счетчик
func ThenCombine[A, B, R any](a *Future[A], b *Future[B], fn func(A, B) R) *Future[R] {
	r := newFuture[R]()
	var (
		av        A
		bv        B
		aerr      error
		berr      error
		remaining int32 = 2
	)

	finish := func() {
		//когда оба отработали (счетчик дошел до 0)
		if atomic.AddInt32(&remaining, -1) == 0 {
			var zero R
			switch {
			case aerr != nil: //ошибка а
				r.complete(zero, aerr)
			case berr != nil: //б
				r.complete(zero, berr)
			default: //все норм
				r.complete(fn(av, bv), nil)
			}
		}
	}

	a.onComplete(func(v A, err error) {
		av, aerr = v, err
		finish()
	})
	b.onComplete(func(v B, err error) {
		bv, berr = v, err
		finish()
	})

	return r
}

//ждем все задачи из списка
func AllOf[T any](fs []*Future[T]) *Future[[]T] {
	r := newFuture[[]T]()
	n := len(fs)
	if n == 0 {
		r.complete([]T{}, nil)
		return r
	}

	out := make([]T, n)
	var (
		mu        sync.Mutex
		firstErr  error
		remaining = int32(n)
	)

	for i, f := range fs {
		idx := i
		f.onComplete(func(v T, err error) {
			mu.Lock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			out[idx] = v
			mu.Unlock()

			//последний завершившийся закрывает общий Future
			if atomic.AddInt32(&remaining, -1) == 0 {
				r.complete(out, firstErr)
			}
		})
	}

	return r
}