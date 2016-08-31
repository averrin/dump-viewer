package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/iris-contrib/template/django"
	"github.com/kataras/iris"
	"github.com/spf13/viper"
)

var ws iris.WebsocketServer
var CMDPORT = 65434
var VERSION string
var sokets []iris.WebsocketConnection

func main() {
	viper.SetConfigType("yaml")
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("Fatal error config file: %s \n", err))
	}
	fmt.Println(viper.Get("commands"))

	iris.Static("/js", "./static/js", 1)
	iris.Static("/css", "./static/css", 1)
	iris.UseTemplate(django.New()).Directory("./templates", ".html")
	iris.Get("/", index)
	iris.Get("/box/:ip", box)
	iris.Get("/dump/:id", dump)
	iris.Get("/box/:ip/dump/:component/:id", getDump)
	iris.Get("/box/:ip/screenshot", getScreenshot)
	iris.Post("/list", list)
	iris.Post("/upload", upload)
	iris.Get("/upload", uploadList)
	iris.Get("/box/:ip/shell", shell)
	// iris.Post("/box/:ip/shell", sendCmd)

	iris.Config.Websocket.Endpoint = "/ws"
	ws = iris.Websocket
	ws.OnConnection(func(c iris.WebsocketConnection) {
		sokets = append(sokets, c)
		c.To(iris.All).Emit("out", []byte(">> Send command\n"))
		c.On("in", func(message string) {
			cmd := strings.Split(message, "//")
			c.To(iris.All).Emit("out",
				[]byte(fmt.Sprintf(">> %s\n", sendCmd(cmd[0], cmd[1]))))
		})
	})

	iris.Listen("0.0.0.0:9900")
}

type Form struct {
	Component string      `form:"component"`
	File      interface{} `form:"file"`
}

func index(ctx *iris.Context) {
	ctx.Render("shell.html", map[string]interface{}{"VERSION": VERSION})
}

func shell(ctx *iris.Context) {
	ip := ctx.Param("ip")
	ctx.Render("shell.html", map[string]interface{}{
		"VERSION": VERSION,
		"IP":      ip,
		"host":    ctx.HostString(),
	})
}

func sendCmd(ip string, cmd string) string {
	conn, data, _ := sendCommand(ip, cmd)
	defer conn.Close()
	result, _ := ioutil.ReadAll(data)
	return string(result)
}

func uploadList(ctx *iris.Context) {
	files, _ := ioutil.ReadDir("./dumps")
	list := []string{}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".txt") {
			list = append(list, strings.Split(f.Name(), ".")[0])
		}
	}
	log.Println(list)
	ctx.Render("index.html", map[string]interface{}{
		"VERSION": VERSION,
		"files":   list,
	})
}

func dump(ctx *iris.Context) {
	id := ctx.Param("id")
	f, err := os.Open(fmt.Sprintf("./dumps/%s.dmp.gz.txt", id))
	if err != nil {
		ctx.Text(500, fmt.Sprintf("%s", err))
	} else {
		defer f.Close()
		// ctx.ServeContent(f, id+".png", time.Now(), false)
		t, _ := ioutil.ReadAll(f)
		ctx.Text(200, string(t))
	}
}

func getScreenshot(ctx *iris.Context) {
	ip := ctx.Param("ip")
	// sendCommand(ip, "resdiag pwr")
	// time.Sleep(3 * time.Second)
	// sendCommand(ip, "resdiag g")
	// time.Sleep(5 * time.Second)
	conn, data, err := sendCommand(ip, "ScreenShot")
	defer conn.Close()
	data.Discard(5)
	var w int32
	var h int32
	var l int32
	binary.Read(data, binary.BigEndian, &w)
	binary.Read(data, binary.BigEndian, &h)
	binary.Read(data, binary.BigEndian, &l)
	archive, err := zlib.NewReader(data)
	log.Println(archive, err)
	defer archive.Close()
	pixelsRaw, _ := ioutil.ReadAll(archive)
	// pixelsRaw = append([]byte{0}, pixelsRaw...)
	log.Println(err)
	go func() {
		f, _ := os.OpenFile("dump.bin", os.O_WRONLY|os.O_CREATE, 0666)
		f.Write(pixelsRaw)
		defer f.Close()
	}()

	pix := make([]byte, 4)
	i := 0
	for n, p := range pixelsRaw {
		switch {
		case i == 0:
			pix[2] = p
		case i == 1:
			pix[1] = p
		case i == 2:
			pix[0] = p
		case i == 3:
			pix[3] = p
		}
		if i == 3 {
			i = 0
			pixelsRaw[n-3] = pix[0]
			pixelsRaw[n-2] = pix[1]
			pixelsRaw[n-1] = pix[2]
			pixelsRaw[n] = pix[3]
		} else {
			i++
		}
	}
	// log.Println(pixelsRaw[:4])
	pixels := bytes.NewBuffer(pixelsRaw)

	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	copy(img.Pix, pixels.Bytes())
	ret := new(bytes.Buffer)
	jpeg.Encode(ret, img, &jpeg.Options{100})
	// png.Encode(ret, img)
	ctx.ServeContent(bytes.NewReader(ret.Bytes()), "screen.png", time.Now(), false)
}

func upload(ctx *iris.Context) {
	form := Form{}
	ctx.ReadForm(&form)
	file, err := ctx.FormFile("file")
	component := form.Component
	if err != nil {
		log.Println(err)
	}
	id := strings.Split(file.Filename, ".")[0]
	re := regexp.MustCompile(`[\da-fA-F]{8}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{8}-[\da-fA-F]{8}`)
	if re.MatchString(id) {
		f, err := os.OpenFile(
			fmt.Sprintf("./dumps/%s.txt", file.Filename), os.O_WRONLY|os.O_CREATE, 0666)
		src, err := file.Open()
		defer f.Close()
		defer src.Close()
		uploadDump(id, component, bufio.NewReader(src))
		time.Sleep(3 * time.Second)
		uri := fmt.Sprintf(
			"http://%s:8080/uhms/stbMinidump?uuid=%s",
			viper.GetString("server"), id,
		)
		response, err := http.Get(uri)
		defer response.Body.Close()
		log.Println(err)
		r, err := ioutil.ReadAll(response.Body)
		f.Write(r)
		log.Println(err)
		ctx.Text(200, string(r))
	} else {
		ctx.Text(500, "Wrong name")
	}
}

func getDump(ctx *iris.Context) {
	id := ctx.Param("id")
	fname := fmt.Sprintf("./cache/%s.txt", id)
	if _, err := os.Stat(fname); os.IsNotExist(err) {
		ip := ctx.Param("ip")
		component := ctx.Param("component")
		log.Println(ip, id, component)
		conn, data, err := sendCommand(ip, "getMiniDump "+id)
		defer conn.Close()
		data.Discard(82)
		uploadDump(id, component, data)
		time.Sleep(3 * time.Second)
		uri := fmt.Sprintf(
			"http://%s:8080/uhms/stbMinidump?uuid=%s",
			viper.GetString("server"), id,
		)
		response, err := http.Get(uri)
		defer response.Body.Close()
		log.Println(err)
		r, err := ioutil.ReadAll(response.Body)
		log.Println(err)
		f, err := os.OpenFile(
			fname, os.O_WRONLY|os.O_CREATE, 0666)
		f.Write(r)
		defer f.Close()
		ctx.Text(200, string(r))
	} else {
		file, _ := os.Open(fname)
		buf := bytes.NewBuffer(nil)
		io.Copy(buf, file)
		ctx.Text(200, string(buf.Bytes()))
	}
}

func box(ctx *iris.Context) {
	ip := ctx.Param("ip")
	ctx.Render("index.html", map[string]interface{}{
		"VERSION": VERSION,
		"IP":      ip,
	})
}

func sendCommand(ip string, command string) (conn net.Conn, result *bufio.Reader, err error) {
	conn, err = net.Dial("tcp", fmt.Sprintf("%s:%d", ip, CMDPORT))
	if err != nil {
		return conn, nil, err
	}
	fmt.Fprintf(conn, command)
	result = bufio.NewReader(conn)
	return conn, result, err
}

func list(ctx *iris.Context) {
	ip := string(ctx.FormValue("ip"))
	conn, data, err := sendCommand(ip, "getMiniDumpList")
	defer conn.Close()
	if err != nil {
		log.Println(err)
		ctx.HTML(500, fmt.Sprintf("%s", err))
	}
	status, _ := data.ReadString('\n')
	status = strings.TrimSpace(status)

	log.Println(status)
	if status == "OK" {
		l, err := data.ReadString('\n')
		l = strings.TrimSpace(l)
		if err != nil {
			log.Println(err)
		}
		dumps := []string{}
		if l == "No dumps found" {
			log.Println(l)
			ctx.JSON(200, struct {
				Dumps []string `json:"dumps"`
				Error string   `json:"error"`
			}{[]string{}, ""})
		} else {
			log.Println(">", l)
			lines := append(dumps, l)
			scanner := bufio.NewScanner(data)
			for scanner.Scan() {
				line := scanner.Text()
				log.Println("|", line)
				lines = append(lines, line)
			}
			dumps := []Dump{}
			for _, l := range lines {
				pairs := strings.Split(l, ", ")
				dump := Dump{}
				dump.Component = strings.Split(pairs[0], ": ")[1]
				dump.ID = strings.Split(pairs[1], ": ")[1]
				dump.Time = strings.Split(pairs[2], ": ")[1]
				dumps = append(dumps, dump)
			}
			ctx.JSON(200, struct {
				Dumps []Dump `json:"dumps"`
				Error string `json:"error"`
			}{dumps, ""})
		}
	}
}

type Dump struct {
	Component string
	ID        string
	Time      string
}

func uploadDump(id string, component string, file *bufio.Reader) {
	uri := fmt.Sprintf("%s:7979", viper.GetString("server"))
	// uri := fmt.Sprintf("%s:7979", "localhost")
	conn, err := net.Dial("tcp", uri)
	mac, err := hex.DecodeString("FFFFFFFFFFFF")
	if err != nil {
		log.Println(err)
	}
	conn.Write(mac)
	c := make([]byte, 32-len(component))
	conn.Write([]byte(component))
	conn.Write(c)
	conn.Write([]byte(id))
	binary.Write(conn, binary.BigEndian, int32(time.Now().Unix()))
	b := bytes.NewBufferString("")
	s, err := io.Copy(b, file)
	log.Println(s)
	binary.Write(conn, binary.BigEndian, int32(s))
	n, err := io.Copy(conn, b)
	log.Println(n)
	if err != nil {
		log.Println(err)
	}
	conn.Close()
}
