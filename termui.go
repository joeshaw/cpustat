package main

import (
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"

	ui "github.com/uber-common/termui"
)

const chartBackingSize = 1024

var sysChart *ui.LineChart
var procChart *ui.LineChart
var mainList *ui.List
var quitChan chan string

var sysChartData = make(map[string][]float64)
var procChartData = make(map[int][]float64)

func tuiFatal(reason string) {
	ui.StopLoop()
	ui.Close()
	quitChan <- reason
	close(quitChan)
}

// these values are from colorbrewer2.org
var colorValues = []string{
	"#9e0142",
	"#d53e4f",
	"#fdae61",
	"#fee08b",
	"#66c2a5",
	"#3288bd",
	"#5e4fa2",
	"#6e5fb2",
	"#7f3b08",
	"#ffffff",
	"#8073ac",
	"#542788",
	"#a6cee3",
	"#33a02c",
	"#b2df8a",
}
var colorList []ui.Attribute
var graphColors map[string]ui.Attribute
var dataLabels []string

func tuiInit(ch chan string, interval int) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			runtime.Stack(buf, false)
			tuiFatal(fmt.Sprintf("%s\n\n%s\n", r, string(buf)))
		}
	}()

	quitChan = ch

	//	ui.DebugFilename = "tuidebug"
	//	ui.Debug("cpustat termui starting...")

	colorList = make([]ui.Attribute, 0)
	for pos, colorStr := range colorValues {
		r, _ := strconv.ParseUint(colorStr[1:3], 16, 8)
		g, _ := strconv.ParseUint(colorStr[3:5], 16, 8)
		b, _ := strconv.ParseUint(colorStr[5:7], 16, 8)
		newAttr := ui.ColorRGB24(int(r), int(g), int(b))
		colorList = append(colorList, newAttr)
		ui.AddColorMap(fmt.Sprintf("color%d", pos), newAttr)
	}

	dataLabels = make([]string, 1024)
	for i := range dataLabels {
		val := fmt.Sprintf("%.1f", math.Abs(float64((i-1023)*interval)/1000.0))
		if strings.HasSuffix(val, ".0") {
			dataLabels[i] = val[:len(val)-2]
		} else {
			dataLabels[i] = val
		}
	}

	sysChartData["usr"] = make([]float64, 0, chartBackingSize)
	sysChartData["sys"] = make([]float64, 0, chartBackingSize)

	if err := ui.Init(); err != nil {
		panic(err)
	}
	ui.SetOutputMode(ui.Output256)

	sysChart = ui.NewLineChart()
	sysChart.Border = true
	sysChart.BorderLabel = "total usr/sys time"
	sysChart.Height = ui.TermHeight() / 2
	sysChart.YFloor = 0.0
	sysChart.LineColor["usr"] = ui.ColorCyan
	sysChart.LineColor["sys"] = ui.ColorRed

	procChart = ui.NewLineChart()
	procChart.Border = true
	procChart.BorderLabel = "top procs"
	procChart.Height = ui.TermHeight() / 2
	procChart.YFloor = 0.0

	mainList = ui.NewList()
	mainList.Border = true
	mainList.Items = []string{"[gathering list of top processes](fg-red,bg-white)"}
	mainList.Height = ui.TermHeight() / 2

	ui.Body.AddRows(
		ui.NewRow(
			ui.NewCol(6, 0, procChart),
			ui.NewCol(6, 0, sysChart),
		),
		ui.NewRow(
			ui.NewCol(12, 0, mainList),
		),
	)

	ui.Body.Align()
	ui.Render(ui.Body)

	ui.Handle("/sys/kbd/q", func(ui.Event) {
		tuiFatal("closing from keyboard")
	})

	ui.Handle("/sys/wnd/resize", func(e ui.Event) {
		mainList.Height = ui.TermHeight() / 2
		procChart.Height = ui.TermHeight() / 2
		sysChart.Height = ui.TermHeight() / 2
		ui.Body.Width = ui.TermWidth()
		ui.Body.Align()
		ui.Render(ui.Body)
	})

	ui.Loop()
}

func tuiListUpdate(list pidlist, sumStats procStatsMap, cmdName cmdlineMap, procHist procStatsHistMap, sysHist *systemStatsHist) {
	defer func() {
		if r := recover(); r != nil {
			tuiFatal(fmt.Sprint(r))
		}
	}()

	graphColors = make(map[string]ui.Attribute)
	mainList.Items = make([]string, len(list))
	colorPos := 0
	for i, pid := range list {
		strPid := fmt.Sprint(pid)
		graphColors[strPid] = colorList[colorPos]
		mainList.Items[i] = fmt.Sprintf("[%d %s %s](fg-color%d)", i, strPid, cmdName[pid].friendly, colorPos)
		colorPos = (colorPos + 1) % len(colorList)
	}

	ui.Render(mainList)
}

func tuiGraphUpdate(sysDelta *systemStats, procDelta procStatsMap, topPids pidlist, jiffy, interval int) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			runtime.Stack(buf, false)
			tuiFatal(fmt.Sprintf("%s\n\n%s\n", r, string(buf)))
		}
	}()

	scale := func(val float64) float64 {
		return val / float64(jiffy) / float64(interval) * 1000 * 100
	}

	sysChartData["usr"] = append(sysChartData["usr"], scale(float64(sysDelta.usr)))
	sysChartData["sys"] = append(sysChartData["sys"], scale(float64(sysDelta.sys)))

	dataPoints := (sysChart.InnerWidth() * 2) - 14 // WTF is this magic number for?
	dataStart := dataPoints
	for name, data := range sysChartData {
		if len(data) < dataPoints {
			dataStart = len(data)
		}
		sysChart.Data[name] = data[len(data)-dataStart:]
	}
	sysChart.DataLabels = dataLabels[len(dataLabels)-dataPoints:]
	ui.Render(sysChart)

	updatedPids := make(map[int]bool)

	for pid, delta := range procDelta {
		updatedPids[pid] = true
		if _, ok := procChartData[pid]; ok == false {
			procChartData[pid] = make([]float64, 1, chartBackingSize)
			procChartData[pid][0] = scale(float64(delta.utime + delta.stime))
		} else {
			procChartData[pid] = append(procChartData[pid], scale(float64(delta.utime+delta.stime)))
		}
	}

	for _, pid := range topPids {
		if updatedPids[pid] == false {
			procChartData[pid] = append(procChartData[pid], 0)
		}
	}

	graphData := make(map[string][]float64)
	for _, pid := range topPids {
		if pid == 0 { // skip uninitialized values
			continue
		}
		data := procChartData[pid]
		dataStart := dataPoints
		if len(data) < dataPoints {
			dataStart = len(data)
		}
		strPid := fmt.Sprint(pid)
		graphData[strPid] = data[len(data)-dataStart:]
	}
	procChart.Data = graphData
	procChart.LineColor = graphColors
	procChart.DataLabels = dataLabels[len(dataLabels)-dataPoints:]
	ui.Render(procChart)
}