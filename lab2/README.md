# Передача файла по TCP с подсчётом скорости

Лабораторная работа: клиент и сервер для передачи произвольного файла
по TCP с измерением скорости приёма на стороне сервера.

## Состав

```
lab2/
├── server/         # сервер
│   ├── go.mod
│   ├── main.go         # точка входа: аргументы, запуск
│   ├── server.go       # Server struct, Run() — accept-loop
│   ├── handler.go      # handleClient() — обработка одного клиента
│   ├── file.go         # receiveFile(), safeFilename()
│   ├── protocol.go     # readUint32/readUint64/sendStatus + лимиты
│   └── speed.go        # speedStats, reportLoop, printReport
│
├── client/         # клиент
│   ├── go.mod
│   ├── main.go         # точка входа: аргументы, запуск
│   ├── client.go       # Client struct, Run(), sendFile()
│   ├── file.go         # openAndStat()
│   └── protocol.go     # writeUint32/writeUint64/readStatus
│
└── README.md
```

## Протокол

Big-endian (сетевой стандарт, RFC 1700). TCP-поток разбит на поля так:

```
КЛИЕНТ → СЕРВЕР:
  [4 байта uint32]  длина имени файла в байтах UTF-8
  [nameLen байт]    имя файла в UTF-8
  [8 байт uint64]   размер файла
  [size байт]       содержимое файла

СЕРВЕР → КЛИЕНТ:
  [1 байт]          статус: 1 = успех, 0 = ошибка
```

Лимиты из ТЗ:

- Длина имени: `maxNameLen = 4096` байт UTF-8
- Размер файла: `maxFileSize = 1 << 40` (1 ТиБ)

## Сборка

```bash
cd ~/seti/lab2/server
go build ./...

cd ~/seti/lab2/client
go build ./...
```

Обе команды должны завершиться без вывода (нет ошибок).

## Запуск

### Терминал 1 — сервер

```bash
cd ~/seti/lab2/server
go run . 9000
```

Ожидаемо:

```
server listening on :9000, saving to "uploads"
```

Сервер сохраняет файлы в `server/uploads/` (создаётся автоматически).

**Останавливать — `Ctrl+C`, не `Ctrl+Z`.** `Ctrl+Z` приостанавливает
процесс, порт остаётся занят, следующий запуск упадёт с
`address already in use`.

### Терминал 2 — клиент

```bash
cd ~/seti/lab2/client
go run . <host> <port> <file>
```

Примеры:

```bash
go run . 127.0.0.1 9000 /tmp/tiny.txt
go run . 127.0.0.1 9000 "/home/slay/Загрузки/5413796024981068416.jpg"
go run . 127.0.0.1 9000 "/home/slay/Загрузки/MOV00000_out.mp4"
```

Если путь содержит пробелы — кавычки обязательны.

## Тестирование

### Подготовка

Терминал 1 — сервер запущен (`go run . 9000`).
Все команды ниже — в терминале 2, из `~/seti/lab2/client`.

### Тест 1. Маленький файл (12 байт)

Проверяет краевой случай: одна итерация цикла `Read`, сеанс короче
секунды (отчёт всё равно печатается — требование ТЗ).

```bash
echo "Hello, TCP!" > /tmp/tiny.txt
go run . 127.0.0.1 9000 /tmp/tiny.txt
```

Ожидаемо у клиента:

```
sent 12 bytes in ... (... B/s)
SUCCESS: file transferred and verified
```

Ожидаемо у сервера:

```
[127.0.0.1:xxxxx] connected
[127.0.0.1:xxxxx] speed: instant=0.00 B/s, average=... B/s, total=12 B
[127.0.0.1:xxxxx] DONE, 12 bytes received
```

`instant=0.00` — сеанс короче 1 секунды, окно не успело обновиться.
Средняя скорость честная.

Проверка:

```bash
cat ~/seti/lab2/server/uploads/tiny.txt
md5sum /tmp/tiny.txt ~/seti/lab2/server/uploads/tiny.txt
```

### Тест 2. Картинка (JPEG)

Проверяет бинарный формат с заголовком и CRC-контрольными суммами
(JPEG). Если хоть один байт искажён — изображение не откроется.

```bash
go run . 127.0.0.1 9000 "/home/slay/Загрузки/5413796024981068416.jpg"
```

Ожидаемо у клиента:

```
sent 179075 bytes in ... (... B/s)
SUCCESS: file transferred and verified
```

Проверка:

```bash
ls -la ~/seti/lab2/server/uploads/5413796024981068416.jpg
md5sum "/home/slay/Загрузки/5413796024981068416.jpg" \
       ~/seti/lab2/server/uploads/5413796024981068416.jpg

xdg-open ~/seti/lab2/server/uploads/5413796024981068416.jpg &
```

Успех: `md5sum` совпал, картинка открывается без ошибок.

### Тест 3. Видео (MP4)

Проверяет mp4-контейнер с `moov`-атомом и кодеками. Малейшее
искажение — плеер не проиграет.

```bash
go run . 127.0.0.1 9000 "/home/slay/Загрузки/MOV00000_out.mp4"
```

Ожидаемо у клиента:

```
sent 6464876 bytes in ... (... B/s)
SUCCESS: file transferred and verified
```

Проверка:

```bash
ls -la ~/seti/lab2/server/uploads/MOV00000_out.mp4
md5sum "/home/slay/Загрузки/MOV00000_out.mp4" \
       ~/seti/lab2/server/uploads/MOV00000_out.mp4

mpv ~/seti/lab2/server/uploads/MOV00000_out.mp4 &
# или
xdg-open ~/seti/lab2/server/uploads/MOV00000_out.mp4
```

Успех: `md5sum` совпал, видео проигрывается до конца.

### Тест 4. Большой файл (200 МБ)

Проверяет много итераций цикла (3200 раз по 64 КБ), длительный сеанс.

```bash
head -c 200M /dev/urandom > /tmp/big.bin
go run . 127.0.0.1 9000 /tmp/big.bin
```

Ожидаемо у клиента:

```
sent 209715200 bytes in ... (... B/s)
SUCCESS: file transferred and verified
```
### Тест 5. 2 файла вместе 

````bash
cd ~/seti/lab2/client

go run . 127.0.0.1 9000 /tmp/big.bin &
go run . 127.0.0.1 9000 "/home/slay/Загрузки/MOV00000_out.mp4" &
wait
````

Проверка целостности:

```bash
md5sum /tmp/big.bin ~/seti/lab2/server/uploads/big.bin
```

Оба хеша должны совпасть. Для 200 МБ MD5 считается ~0.5 секунды.

На loopback 200 МБ пройдут за ~0.1 секунды — сервер напечатает
один финальный отчёт с `instant=0.00`.

#### Дополнительно: несколько отчётов скорости

Чтобы увидеть несколько отчётов раз в 3 секунды (с ненулевым
`instant`), замедлите loopback через `tc`:

```bash
sudo tc qdisc add dev lo root netem rate 200mbit
go run . 127.0.0.1 9000 /tmp/big.bin
sudo tc qdisc del dev lo root
```

200 МБ при 200 Мбит/с ≈ 8 секунд → сервер напечатает **2–3 отчёта**,
`instant` будет ~25 МБ/с.

### Проверка параллельной работы

Требование ТЗ: сервер работает с несколькими клиентами одновременно.

Откройте **два дополнительных терминала**:

```bash
# Терминал 2
cd ~/seti/lab2/client
go run . 127.0.0.1 9000 "/home/slay/Загрузки/MOV00000_out.mp4"

# Терминал 3 (одновременно)
cd ~/seti/lab2/client
go run . 127.0.0.1 9000 "/home/slay/Загрузки/5413796024981068416.jpg"
```

В логе сервера увидите **два разных `clientID`** с **отдельными**
строками `speed` — это и есть параллельная работа.

### Проверка обработки ошибок

Несуществующий файл — клиент должен упасть с понятным сообщением:

```bash
go run . 127.0.0.1 9000 /tmp/doesnotexist.bin
# client: open file: open /tmp/doesnotexist.bin: no such file or directory
# exit status 1
```

Опасное имя (path traversal) — сервер должен отклонить:

```bash
echo "test" > "/tmp/..evil"
go run . 127.0.0.1 9000 "/tmp/..evil"
# сервер: unsafe filename "..evil": empty or traversal name
```

### Универсальный скрипт

Сохраните как `~/seti/lab2/test_all.sh`:

```bash
#!/bin/bash
set -e

HOST=127.0.0.1
PORT=9000
UPLOADS="$HOME/seti/lab2/server/uploads"
CLIENT_DIR="$HOME/seti/lab2/client"
IMG="/home/slay/Загрузки/5413796024981068416.jpg"
VID="/home/slay/Загрузки/MOV00000_out.mp4"

[ -f /tmp/tiny.txt ]   || echo "Hello, TCP!" > /tmp/tiny.txt
[ -f /tmp/big.bin ]    || head -c 200M /dev/urandom > /tmp/big.bin

cd "$CLIENT_DIR"

test_file() {
    local path=$1
    local name
    name=$(basename "$path")
    local size
    size=$(stat -c %s "$path")

    echo "=== $name ($size байт) ==="
    go run . "$HOST" "$PORT" "$path"

    local orig recv
    orig=$(md5sum "$path" | awk '{print $1}')
    recv=$(md5sum "$UPLOADS/$name" | awk '{print $1}')

    if [ "$orig" = "$recv" ]; then
        echo "  OK: md5 совпал"
    else
        echo "  FAIL: md5 различается"
        echo "    orig: $orig"
        echo "    recv: $recv"
        exit 1
    fi
    echo
}

test_file /tmp/tiny.txt
[ -f "$IMG" ] && test_file "$IMG"
[ -f "$VID" ] && test_file "$VID"
test_file /tmp/big.bin

echo "Все тесты прошли"
```

Запуск:

```bash
chmod +x ~/seti/lab2/test_all.sh
~/seti/lab2/test_all.sh
```

## Соответствие требованиям ТЗ

| № | Требование | Где реализовано |
|---|---|---|
| 1 | Порт сервера в параметрах | `server/main.go` |
| 2 | Путь у клиента, лимиты 4096 Б / 1 ТиБ | `client/main.go`, `client/file.go` |
| 3 | DNS-имя/IP и порт у клиента | `client/main.go` |
| 4 | Клиент передаёт имя UTF-8, размер, содержимое по TCP | `client/client.go` |
| 5 | Сохранение в `uploads/`, имя совпадает, не за пределы | `server/server.go`, `server/file.go` |
| 6 | Скорость раз в 3 с, мгновенная и средняя, отдельно, для коротких | `server/speed.go` |
| 7 | Проверка размера, сообщение клиенту, закрытие | `server/handler.go` |
| 8 | Клиент печатает результат | `client/client.go`, `client/protocol.go` |
| 9 | Освобождение ресурсов | `defer` + `close(stop)`/`<-reportDone` |
| 10 | Параллельная работа через треды | `server/server.go` (`go s.handleClient(c)`) |
| 11 | При ошибке разорвать, продолжить работу | `server/handler.go`, `server/server.go` |

## Известные ограничения

1. **`os.Exit(2)` в `readStatus`** (клиент). При `statusFail` программа
   завершается до выполнения `defer conn.Close()` и `defer f.Close()` в
   `Run`. ОС закрывает дескрипторы при завершении процесса — утечек
   нет, но формально через `defer` не освобождается. Для CLI это
   допустимо. При желании можно переделать на sentinel-ошибку
   `ErrServerRejected` и обрабатывать в `main`.

2. **Нет таймаутов на чтение/запись** на сервере. Если клиент
   подключится и замолчит, горутина `handleClient` будет висеть,
   держа дескриптор. Для продакшена нужен `conn.SetReadDeadline`.

3. **`SO_REUSEADDR` не установлен.** После `Ctrl+Z` или падения
   сервер может ругаться `address already in use`. Решение —
   останавливать через `Ctrl+C` или `pkill -9 -x server`.

4. **Одновременное закрытие TCP** (вариант 3 из классификации
   завершения). На loopback теоретически возможно, но редко.

5. **Симлинки в `uploads/` не проверяются.** Если файл `uploads/x` уже
   существует как симлинк на `/etc/passwd`, `os.Create` перезапишет
   цель. Для продакшена нужен `O_NOFOLLOW`.

## Быстрая команда для остановки сервера

Если порт занят:

```bash
ss -ltnp | grep 9000
kill -9 <PID>
```

Или всё сразу:

```bash
pkill -9 -x server
pkill -9 -f "go run"
```
