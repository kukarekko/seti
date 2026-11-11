package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/jzx17/gofetch"
)

const (
	userAgent = "PlacesApp/1.0 (https://github.com/kukarekko/places; polyavor@gmail.com)"
	maxBodySize = 5 << 20
	maxParallelRequests = 6
	requestTimeout = 15 * time.Second
)

// запрос отправляет gofetch.Client.GetAsync, который сразу возвращает канал завершения


//структура с асинхр клиентом и семафора 
type AsyncClient struct {
	transport *gofetch.Client
	sem chan struct{} // ограничение числа одновр запросов
}

//создает асинхронного клиента
func NewAsyncClient(timeout time.Duration, maxParallel int, opts ...gofetch.Option) *AsyncClient {
	size := gofetch.DefaultSizeConfig()
	size.MaxResponseBodySize = maxBodySize
	base := []gofetch.Option{  //список опций 
		gofetch.WithTimeout(timeout), 
		gofetch.WithAutoBufferResponse(true), //автоматически читать тело в буфер
		gofetch.WithSizeConfig(size), //лимит размера
	}
	return &AsyncClient{
		transport: gofetch.NewClient(append(base, opts...)...),
		sem: make(chan struct{}, maxParallel),
	}
}

// глобальный клиент
var apiClient = NewAsyncClient(requestTimeout, maxParallelRequests)

//метод возвращается немедленно, по завершению 1 раз выызывается cd из горутины клиента
func (c *AsyncClient) GetAsync(ctx context.Context, u string, cb func(body []byte, err error)) {
	go func() { 
		cb(c.await(ctx, u)) 
	}()
}

//то же самое, но результат отдаётся в виде Future
func (c *AsyncClient) Get(ctx context.Context, u string) *Future[[]byte] {
	f := newFuture[[]byte]()
	c.GetAsync(ctx, u, f.complete)
	return f
}

//ждем ответ от gofetch
func (c *AsyncClient) await(ctx context.Context, u string) ([]byte, error) {
	select {
	case c.sem <- struct{}{}: //пытаемся занять место в семафоре
		defer func() { <-c.sem }()
	case <-ctx.Done(): //ошибка 
		return nil, ctx.Err()
	}

	done := c.transport.GetAsync(ctx, u, map[string]string{"User-Agent": userAgent}) //возвр канал done по которому прийдет результат
	res, ok := <-done   //ждем результат
	if !ok {
		return nil, fmt.Errorf("HTTP-клиент закрылся без результата")
	}
	if res.Error != nil {
		return nil, redact(res.Error)
	}
	resp := res.Response
	if resp == nil {
		return nil, fmt.Errorf("HTTP-клиент не вернул ответ")
	}
	defer resp.CloseBody() //закрываем тело ответа, когда закончили

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("сервер вернул HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1)) //ситаем не более 5 мб
	if err != nil {
		return nil, redact(err)
	}
	if len(body) > maxBodySize {
		return nil, fmt.Errorf("ответ больше %d байт", maxBodySize)
	}
	return body, nil
}


var secretRe = regexp.MustCompile(`(?i)(apikey|appid)=[^&"\s]+`)

//вырезаем ключи 
func redact(err error) error {
	return fmt.Errorf("%s", secretRe.ReplaceAllString(err.Error(), "$1=***"))
}

// GET и разбор JSON в T, возвр Future сразу
func getJSON[T any](ctx context.Context, u string) *Future[T] {
	f := newFuture[T]()
	apiClient.GetAsync(ctx, u, func(body []byte, err error) {
		var out T
		if err == nil {
			err = json.Unmarshal(body, &out)
		}
		f.complete(out, err)
	})
	return f
}

// GET и декодирование и уменьшение картинки, возвр Future сразу
func getImage(ctx context.Context, u string, maxW, maxH int) *Future[*image.RGBA] {
	f := newFuture[*image.RGBA]()
	apiClient.GetAsync(ctx, u, func(body []byte, err error) {
		if err != nil {
			f.complete(nil, err)
			return
		}
		f.complete(decodeImage(body, maxW, maxH))
	})
	return f
}

//декодирование картинки
func decodeImage(body []byte, maxW, maxH int) (img *image.RGBA, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("ошибка декодирования картинки: %v", r)
		}
	}()
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	return resizeFit(src, maxW, maxH), nil
}