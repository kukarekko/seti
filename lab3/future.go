package main

import "fmt"

type Future[T any] struct {
	done chan struct{}
	val  T
	err  error
}

func newFuture[T any]() *Future[T] { return &Future[T]{done: make(chan struct{})} }

func (f *Future[T]) complete(v T, err error) {
	f.val, f.err = v, err
	close(f.done)
}

//ожидание результата
func (f *Future[T]) Await() (T, error) {
	<-f.done
	return f.val, f.err
}

func (f *Future[T]) orElse(fn func(error) T) *Future[T] {
	r := newFuture[T]()
	go func() {
		v, err := f.Await()
		if err != nil {
			v = fn(err)
		}
		r.complete(v, nil)
	}()
	return r
}

//создаёт пустой Future, запускает fn в отдельной горутине
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

//преобразовываем тип футур для результата
func Then[T, U any](f *Future[T], fn func(T) (U, error)) *Future[U] { 
	r := newFuture[U]()
	go func() {
		v, err := f.Await()
		if err != nil {
			var zero U
			r.complete(zero, err)
			return
		}
		r.complete(fn(v))
	}()
	return r
}

func ThenCompose[T, U any](f *Future[T], fn func(T) *Future[U]) *Future[U] {
	r := newFuture[U]()
	go func() {
		v, err := f.Await()
		if err != nil {
			var zero U
			r.complete(zero, err)
			return
		}
		r.complete(fn(v).Await())
	}()
	return r
}

//берёт два независимых Future и ждёт, пока оба закончат
func ThenCombine[A, B, R any](a *Future[A], b *Future[B], fn func(A, B) R) *Future[R] {
	r := newFuture[R]()
	go func() {
		av, aerr := a.Await()
		bv, berr := b.Await()
		var zero R
		switch {
		case aerr != nil:
			r.complete(zero, aerr)
		case berr != nil:
			r.complete(zero, berr)
		default:
			r.complete(fn(av, bv), nil)
		}
	}()
	return r
}

//ждем все
func AllOf[T any](fs []*Future[T]) *Future[[]T] {
	r := newFuture[[]T]()
	go func() {
		out := make([]T, len(fs))
		var firstErr error
		for i, f := range fs {
			v, err := f.Await()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			out[i] = v
		}
		r.complete(out, firstErr)
	}()
	return r
}
