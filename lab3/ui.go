package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"

	"github.com/andlabs/ui"
)

type placesModel struct {
	places []Place
	images []*ui.Image
}

func (m *placesModel) ColumnTypes(*ui.TableModel) []ui.TableValue {
	return []ui.TableValue{
		ui.TableImage{},
		ui.TableString(""),
		ui.TableString(""),
		ui.TableString(""),
	}
}

func (m *placesModel) NumRows(*ui.TableModel) int { return len(m.places) }

func (m *placesModel) CellValue(_ *ui.TableModel, row, col int) ui.TableValue {
	p := m.places[row]
	switch col {
	case 0:
		return ui.TableImage{I: m.images[row]}
	case 1:
		return ui.TableString(fmt.Sprintf("%d. %s", row+1, p.Name))
	case 2:
		return ui.TableString(fmt.Sprintf("%.0f м", p.Dist))
	default:
		return ui.TableString(wrapText(shorten(p.Description, 220), 42, ""))
	}
}

func (m *placesModel) SetCellValue(*ui.TableModel, int, int, ui.TableValue) {}

func runGUI(geoKey, owmKey string) {
	err := ui.Main(func() {
		window := ui.NewWindow("Places: Погода и Интересные места", 1100, 800, false)
		window.SetMargined(true)

		queryEntry := ui.NewEntry()
		searchBtn := ui.NewButton("Найти")

		resultLabel := ui.NewLabel("")

		weatherGroup := ui.NewGroup("Погода")
		weatherGroup.SetMargined(true)
		weatherGroup.SetChild(resultLabel)

		comboHolder := ui.NewVerticalBox()
		var foundLocs []Location
		var searchID, loadID int
		var hasCombo bool

		// Заглушка для картинок (светло-серая)
		placeholder := ui.NewImage(120, 90)
		gray := image.NewRGBA(image.Rect(0, 0, 120, 90))
		draw.Draw(gray, gray.Bounds(), &image.Uniform{color.RGBA{238, 238, 238, 255}}, image.Point{}, draw.Src)
		placeholder.Append(gray)

		handler := &placesModel{}
		model := ui.NewTableModel(handler)
		table := ui.NewTable(&ui.TableParams{Model: model, RowBackgroundColorModelColumn: -1})
		table.AppendImageColumn("Фото", 0)
		table.AppendTextColumn("Название", 1, ui.TableModelColumnNeverEditable, nil)
		table.AppendTextColumn("Расстояние", 2, ui.TableModelColumnNeverEditable, nil)
		table.AppendTextColumn("Описание", 3, ui.TableModelColumnNeverEditable, nil)

		clearPlaces := func() {
			for i := len(handler.places) - 1; i >= 0; i-- {
				handler.places = handler.places[:i]
				handler.images = handler.images[:i]
				model.RowDeleted(i)
			}
		}

		addPlace := func(p Place) {
			img := placeholder
			if p.Image != nil {
				img = ui.NewImage(float64(p.Image.Bounds().Dx()), float64(p.Image.Bounds().Dy()))
				img.Append(p.Image)
			}
			handler.places = append(handler.places, p)
			handler.images = append(handler.images, img)
			model.RowInserted(len(handler.places) - 1)
		}

		showReport := func(loc Location) {
			loadID++
			id := loadID
			resultLabel.SetText("Загружаю погоду и места...")
			clearPlaces()

			go func() {
				report, err := loadReport(loc, owmKey, geoKey).Await()
				ui.QueueMain(func() {
					if id != loadID {
						return
					}
					if err != nil {
						resultLabel.SetText(fmt.Sprintf("Ошибка загрузки данных: %v", err))
						return
					}

					w := report.Weather
					var sb strings.Builder
					sb.WriteString(fmt.Sprintf("📍 %s\n\n", report.Location.Name))
					sb.WriteString(fmt.Sprintf("🌤 Погода: %s, %.1f °C (ощущается как %.1f °C)\n", w.Description, w.Temp, w.Feels))
					sb.WriteString(fmt.Sprintf("💧 Влажность: %d %%\n", w.Humidity))
					sb.WriteString(fmt.Sprintf("🌪 Давление: %.0f мм рт. ст.\n", float64(w.Pressure)*0.750062))
					sb.WriteString(fmt.Sprintf("💨 Ветер: %.1f м/с\n\n", w.WindSpeed))
					if len(report.Places) == 0 {
						sb.WriteString("🏛 Интересных мест рядом не найдено.")
					} else {
						sb.WriteString("🏛 ИНТЕРЕСНЫЕ МЕСТА РЯДОМ (радиус 1000 м) — в таблице ниже")
					}
					resultLabel.SetText(sb.String())

					for _, p := range report.Places {
						addPlace(p)
					}
				})
			}()
		}

		box := ui.NewVerticalBox()
		box.SetPadded(true)
		box.Append(ui.NewLabel("Введите название:"), false)
		box.Append(queryEntry, false)
		box.Append(searchBtn, false)
		box.Append(comboHolder, false)
		box.Append(weatherGroup, false)
		box.Append(table, true)
		window.SetChild(box)

		searchBtn.OnClicked(func(*ui.Button) {
			query := strings.TrimSpace(queryEntry.Text())
			if query == "" {
				ui.MsgBoxError(window, "Ошибка", "Введите запрос")
				return
			}

			searchID++
			id := searchID
			resultLabel.SetText("Ищу локации...")

			go func() {
				locs, err := searchLocations(query, geoKey).Await()
				ui.QueueMain(func() {
					if id != searchID {
						return
					}
					if hasCombo {
						comboHolder.Delete(0)
						hasCombo = false
					}
					foundLocs = nil

					if err != nil {
						resultLabel.SetText(fmt.Sprintf("Ошибка поиска: %v", err))
						return
					}

					foundLocs = locs
					combo := ui.NewCombobox()
					for i, l := range locs {
						kind := l.Kind
						if kind == "" {
							kind = "место"
						}
						combo.Append(fmt.Sprintf("%d. %s [%s]", i+1, l.Name, kind))
					}
					combo.SetSelected(-1)
					combo.OnSelected(func(c *ui.Combobox) {
						idx := c.Selected()
						if idx < 0 || idx >= len(foundLocs) {
							return
						}
						showReport(foundLocs[idx])
					})
					comboHolder.Append(combo, false)
					hasCombo = true
					resultLabel.SetText("Выберите локацию из списка ниже:")
				})
			}()
		})

		window.OnClosing(func(*ui.Window) bool {
			ui.Quit()
			return true
		})
		window.Show()
	})
	if err != nil {
		panic(err)
	}
}