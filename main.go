package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tealeg/xlsx"

	"github.com/chentiangang/xlog"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Stock struct {
	StockCurrentPrice *prometheus.Desc
	StockCurrentADR   *prometheus.Desc
}

var stocks map[string]string

func NewStock() *Stock {
	return &Stock{
		StockCurrentPrice: prometheus.NewDesc("stock_current_price",
			"query stock current price",
			[]string{"name", "code"},
			prometheus.Labels{},
		),
		StockCurrentADR: prometheus.NewDesc("stock_current_adr",
			"stock Rate of rise and fall",
			[]string{"name", "code"},
			prometheus.Labels{},
		),
	}
}

func getShAll(url string) ([]byte, error) {
	client := http.Client{}
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Add("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/73.0.3683.103 Safari/537.36")
	request.Header.Add("Host", "query.sse.com.cn")
	request.Header.Add("Connection", "keep-alive")
	request.Header.Add("Accept", "*/*")
	request.Header.Add("Origin", "http://www.sse.com.cn")
	request.Header.Add("Referer", "http://www.sse.com.cn/assortment/stock/list/share/") //关键头 如果没有 则返回 错误
	request.Header.Add("Accept-Encoding", "gzip, deflate")
	request.Header.Add("Accept-Language", "zh-CN,zh;q=0.9")
	resp, _ := client.Do(request)
	defer resp.Body.Close()

	// 将GBK编码转为UTF8
	body := bufio.NewReader(resp.Body)
	utf8Reader := transform.NewReader(body, simplifiedchinese.GBK.NewDecoder())

	res, err := ioutil.ReadAll(utf8Reader)
	if err != nil {
		return nil, err
	}
	return res, err
}

func GetShList() {
	body, err := getShAll("http://query.sse.com.cn/security/stock/downloadStockListFile.do?csrcCode=&stockCode=&areaName=&stockType=1")
	if err != nil {
		panic(err)
	}
	bodyList := strings.Split(string(body), "\n")

	re := regexp.MustCompile("\\d{6}")
	for _, i := range bodyList {
		lineList := strings.FieldsFunc(i, func(r rune) bool {
			return r == ' ' || r == '\t'
		})

		for k, j := range lineList {
			if re.Match([]byte(j)) {
				stocks["sh"+j] = lineList[k+1]
			}
		}
	}
}

func Request(suffix string) (result string) {
	client := &http.Client{}

	req, err := http.NewRequest("GET", "http://hq.sinajs.cn/list="+suffix, nil)
	if err != nil {
		xlog.LogError("%s", err)
	}

	req.Header.Add("User-AgenUser-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_14_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/83.0.4103.116 Safari/537.36")
	reps, err := client.Do(req)
	if err != nil {
		xlog.LogError("%s", err)
		return
	}
	if reps.StatusCode != http.StatusOK {
		xlog.LogError("%s", err)
		return
	}

	body := bufio.NewReader(reps.Body)

	res, err := ioutil.ReadAll(body)
	reps.Body.Close()
	if err != nil {
		xlog.LogError("%s", err)
		return
	}

	return string(res)
}

type Result struct {
	CurrentPrice string
	ClosePrice   string
}

func ParseResult(result string) Result {
	if result == "" {
		xlog.LogError("result: %s", result)
		return Result{}
	}
	split := strings.Split(result, "=")
	result = split[1]
	list := strings.FieldsFunc(result, func(r rune) bool {
		return r == '"' || r == ',' || r == ';'
	})

	if len(list) != 34 {
		xlog.LogError("list: %s,{len: %d},result: %s", list, len(list), result)
		return Result{}
	}
	return Result{ClosePrice: list[2], CurrentPrice: list[3]}
}

func (r Result) currentPrice() float64 {
	return parseFloat(r.CurrentPrice)
}

func (r Result) closePrice() float64 {
	return parseFloat(r.ClosePrice)
}

func (r Result) ADR() float64 {
	return (r.currentPrice() - r.closePrice()) / r.closePrice() * 100
}

func parseFloat(str string) float64 {
	f, err := strconv.ParseFloat(str, 64)
	if err != nil {
		xlog.LogError("%s", err)
		return 0.00
	}
	return f
}

func (s *Stock) Describe(ch chan<- *prometheus.Desc) {
	ch <- s.StockCurrentPrice
	ch <- s.StockCurrentADR
}

func (s *Stock) Collect(ch chan<- prometheus.Metric) {
	wg := sync.WaitGroup{}
	wg.Add(len(stocks))
	for k, v := range stocks {
		go func(suffix string, name string) {
			r := ParseResult(Request(suffix))
			//xlog.LogDebug("request: http://hq.sinajs.cn/list=%s", suffix)
			ch <- prometheus.MustNewConstMetric(s.StockCurrentPrice,
				prometheus.GaugeValue,
				r.currentPrice(),
				name,
				suffix,
			)

			ch <- prometheus.MustNewConstMetric(s.StockCurrentADR,
				prometheus.GaugeValue,
				r.ADR(),
				name,
				suffix,
			)
			//fmt.Printf("url: http://hq.sinajs.cn/list=%s\n", suffix)
			wg.Done()
		}(k, v)
	}
	wg.Wait()
}

func getSzList() {
	xlsxFile := "./A股列表.xlsx"
	txtFile := "./szlist.txt"
	xlFile, err := xlsx.OpenFile(xlsxFile)
	if err != nil {
		panic(err)
	}

	tFile, err := os.OpenFile(txtFile, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		panic(err)
	}

	w := bufio.NewWriter(tFile)
	defer w.Flush()

	for _, sheet := range xlFile.Sheets {
		for _, row := range sheet.Rows {
			matchRe := `\d{6}`
			re := regexp.MustCompile(matchRe)
			if re.Match([]byte(row.Cells[4].Value)) {
				w.WriteString("sz" + row.Cells[4].Value + "," + row.Cells[5].Value + "\n")
			}
		}
	}
}

func GetSzList() {
	client := &http.Client{}

	req, err := http.NewRequest("GET", "https://raw.githubusercontent.com/chentiangang/stock_exporter/master/szlist.txt", nil)
	if err != nil {
		xlog.LogError("%s", err)
	}

	reps, err := client.Do(req)
	if err != nil {
		xlog.LogError("%s", err)
		return
	}

	result, err := ioutil.ReadAll(reps.Body)
	if err != nil {
		panic(err)
	}

	split := strings.Split(string(result), "\n")

	for _, i := range split {
		v := strings.Split(i, ",")
		if len(v) == 2 {
			stocks[v[0]] = v[1]
		}
	}

}

func main() {

	stock := NewStock()
	stocks = make(map[string]string, 2000)
	var exchange string
	var port int
	flag.StringVar(&exchange, "e", "all", "")
	flag.IntVar(&port, "p", 8080, "")
	flag.Parse()

	switch exchange {
	case "sh":
		GetShList()
	case "sz":
		GetSzList()
	default:
		GetShList()
		GetSzList()
	}

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(stock)
	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}
