package main

import (
	"encoding/json"
	"github.com/gorilla/mux"
	"gopkg.in/mgo.v2"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

const (
	MONGO_URL             = "mongodb://127.0.0.1:27017/rtmonitor"
	MONGO_CONNECT_TIMEOUT = 2 * time.Second
)

var (
	signal_chan chan os.Signal // 处理信号的channel
	MgoSession  *mgo.Session
	REPORT_TYPE = []string{"load", "process", "runtime"}
)

func GetCollection(collection_name string) *mgo.Collection {
	return MgoSession.DB("").C(collection_name)
}

func CreateCollection(collection_name string, size, max int) error {
	collection := GetCollection(collection_name)

	collection_info := mgo.CollectionInfo{DisableIdIndex: false, ForceIdIndex: true, Capped: true, MaxBytes: size, MaxDocs: max}

	err := collection.Create(&collection_info)
	if err != nil {
		return err
	}

	return nil
}

func init() {
	var err error

	MgoSession, err = mgo.DialWithTimeout(MONGO_URL, MONGO_CONNECT_TIMEOUT)
	if err != nil {
		log.Printf("Connect to [%s] failed: %s\n", MONGO_URL, err)
		os.Exit(1)
	}

	err = MgoSession.Ping()
	if err != nil {
		log.Println("Check connection failed:", err)
		os.Exit(1)
	}
}

type CollInfo struct {
	Name string
	Size int
	Max  int
}

// 系统信息
type SystemInfo struct {
	Hostname     string `json:"host_name"`
	Sysname      string `json:"sysname"`
	Release      string `json:"release"`
	Machine      string `json:"machine"`
	CPUModelName string `json:"cpu_model_name"`
	CPUFrequency string `json:"cpu_frequency"`
	CPUCores     int    `json:"cpu_cores"`
	Location     string `json:"Location"`
	GOVersion    string `json:"go_version"`
	ProcessID    int    `json:"process_id"`
	CmdLine      string `json:"command_line"`
}

// 内存和CPU负载信息
type LoadInfo struct {
	TimeString string  `json:"time_string"`
	MemTotal   uint64  `json:"memory_total"`
	MemUsed    uint64  `json:"memory_used"`
	MemFree    uint64  `json:"memory_free"`
	MemBuffers uint64  `json:"memory_buffers"`
	MemCached  uint64  `json:"memory_cached"`
	LoadAVG1   float64 `json:"load_avg_1"`
	LoadAVG5   float64 `json:"load_avg_5"`
	LoadAVG15  float64 `json:"load_avg_15"`
}

// 进程的内存占用等
type ProcessInfo struct {
	TimeString      string `json:"time_string"`
	Uptime          int64  `json:"uptime"`
	VirtualMemory   int64  `json:"virtual_memory"`
	ResisdentMemory int64  `json:"resident_memory"`
	SharedMemory    int64  `json:"shared_memory"`
}

// golang运行时的内存状态
type RuntimeStatus struct {
	TimeString string `json:"time_string"`
	// General statistics
	Alloc   uint64 `json:"alloc_bytes"`
	Sys     uint64 `json:"sys_bytes"`
	Mallocs uint64 `json:"mallocs_bytes"`
	Frees   uint64 `json:"frees_bytes"`

	// Main allocation heap statistics
	HeapAlloc   uint64 `json:"heap_alloc_bytes"`
	HeapSys     uint64 `json:"heap_sys_bytes"`
	HeapIdle    uint64 `json:"heap_idle_bytes"`
	HeapInuse   uint64 `json:"heap_inuse_bytes"`
	HeapObjects uint64 `json:"heap_objests"`

	// stack statistics
	StackInuse  uint64 `json:"stack_inuse_bytes"`
	StackSys    uint64 `json:"stack_sys_bytes"`
	MSpanInuse  uint64 `json:"mspan_inuse_bytes"`
	MSpanSys    uint64 `json:"mspan_sys_bytes"`
	MCacheInuse uint64 `json:"mcache_inuse_bytes"`
	MCacheSys   uint64 `json:"mcache_sys_bytes"`

	// GC status
	GCPause          float64 `json:"gc_pause"`
	GCPausePerSecond float64 `json:"gc_pause_per_second"`
	GCPerSecond      float64 `json:"gc_per_second"`
	GCTotalPause     float64 `json:"gc_total_pause"`

	//Num of goroutines
	Goroutines uint64 `json:"goroutines"`
}

func GenerateUserCollection(client_key string) []*CollInfo {
	var user_all_collection []*CollInfo

	/*
		1小时数据: 5秒/条，720条
		4小时数据: 10秒/条，1440条
		24小时数据: 60秒/条，1440条
		48小时数据: 180秒/条，960条
	*/

	for idx := range REPORT_TYPE {
		TYPE := REPORT_TYPE[idx]

		coll_info_1_hour := &CollInfo{Name: client_key + "_" + TYPE + "_" + "1_hour", Size: 4096, Max: 720}
		user_all_collection = append(user_all_collection, coll_info_1_hour)

		coll_info_4_hours := &CollInfo{Name: client_key + "_" + TYPE + "_" + "4_hours", Size: 8192, Max: 1440}
		user_all_collection = append(user_all_collection, coll_info_4_hours)

		coll_info_24_hours := &CollInfo{Name: client_key + "_" + TYPE + "_" + "24_hours", Size: 8192, Max: 1440}
		user_all_collection = append(user_all_collection, coll_info_24_hours)

		coll_info_48_hours := &CollInfo{Name: client_key + "_" + TYPE + "_" + "48_hours", Size: 4096, Max: 960}
		user_all_collection = append(user_all_collection, coll_info_48_hours)
	}

	return user_all_collection
}

func ClientCreateHandler(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			log.Println(err)
			debug.PrintStack()
		}
	}()

	vars := mux.Vars(req)
	client_key := vars["CLIENT_KEY"]

	user_collection_info := GenerateUserCollection(client_key)

	for idx := range user_collection_info {
		coll := user_collection_info[idx]
		err := CreateCollection(coll.Name, coll.Size, coll.Max)
		if err != nil {
			log.Printf("User [%s] create collection failed: [%s]\n", client_key, err)
		}
	}
}

func SystemReportHandler(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			log.Println(err)
			debug.PrintStack()
		}
	}()

	var system_info SystemInfo

	/*
		vars := mux.Vars(req)
		CLIENT_KEY := vars["CLIENT_KEY"]
		log.Println("CLIENT_KEY")
	*/

	buf, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Printf("Read data from: [%s] failed.\n", req.RemoteAddr)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = json.Unmarshal(buf, &system_info)
	if err != nil {
		log.Printf("[%s] Unmarshal json failed.\n", req.RemoteAddr)
		http.Error(w, "Unmarshal json failed.", 500)
		return
	}

	log.Println("SystemInfo:", system_info)

}

func LoadReportHandler(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			log.Println(err)
			debug.PrintStack()
		}
	}()

	var load_info LoadInfo

	buf, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Printf("Read data from: [%s] failed.\n", req.RemoteAddr)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = json.Unmarshal(buf, &load_info)
	if err != nil {
		log.Printf("[%s] Unmarshal json failed.\n", req.RemoteAddr)
		http.Error(w, "Unmarshal json failed.", 500)
		return
	}

	log.Println("LoadInfo:", load_info)
}

func ProcessReportHandler(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			log.Println(err)
			debug.PrintStack()
		}
	}()

	var process_info ProcessInfo

	/*
		vars := mux.Vars(req)
		CLIENT_KEY := vars["CLIENT_KEY"]
		log.Println("CLIENT_KEY")
	*/

	buf, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Printf("Read data from: [%s] failed.\n", req.RemoteAddr)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = json.Unmarshal(buf, &process_info)
	if err != nil {
		log.Printf("[%s] Unmarshal json failed.\n", req.RemoteAddr)
		http.Error(w, "Unmarshal json failed.", 500)
		return
	}

	log.Println("ProcessInfo:", process_info)
}

func RuntimeReportHandler(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			log.Println(err)
			debug.PrintStack()
		}
	}()

	var runtime_status RuntimeStatus

	/*
		vars := mux.Vars(req)
		CLIENT_KEY := vars["CLIENT_KEY"]
		log.Println("CLIENT_KEY")
	*/

	buf, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Printf("Read data from: [%s] failed.\n", req.RemoteAddr)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = json.Unmarshal(buf, &runtime_status)
	if err != nil {
		log.Printf("[%s] Unmarshal json failed.\n", req.RemoteAddr)
		http.Error(w, "Unmarshal json failed.", 500)
		return
	}

	log.Println("RuntimeStatus:", runtime_status)

}

// 信号回调
func signalCallback() {
	for s := range signal_chan {
		sig := s.String()
		log.Println("Got Signal: " + sig)

		if s == syscall.SIGINT || s == syscall.SIGTERM {
			log.Println("Server exit...")
			os.Exit(0)
		}
	}
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			log.Println(err)
			debug.PrintStack()
		}
	}()

	runtime.GOMAXPROCS(runtime.NumCPU())

	// HOLD住POSIX SIGNAL
	signal_chan = make(chan os.Signal, 10)
	signal.Notify(signal_chan,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		syscall.SIGPIPE,
		syscall.SIGALRM,
		syscall.SIGPIPE,
		syscall.SIGBUS,
		syscall.SIGCHLD,
		syscall.SIGCONT,
		syscall.SIGFPE,
		syscall.SIGILL,
		syscall.SIGIO,
		syscall.SIGIOT,
		syscall.SIGPROF,
		syscall.SIGSEGV,
		syscall.SIGSTOP,
		syscall.SIGSYS,
		syscall.SIGTRAP,
		syscall.SIGURG,
		syscall.SIGUSR1,
		syscall.SIGUSR2)

	go signalCallback()

	// 启动性能调试接口
	go func() {
		http.ListenAndServe("0.0.0.0:9899", nil)
	}()

	router := mux.NewRouter()

	s := &http.Server{
		Addr:           "0.0.0.0:9898",
		Handler:        router,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	router.HandleFunc("/api/{CLIENT_KEY}/report/system/", SystemReportHandler).Methods("POST")
	router.HandleFunc("/api/{CLIENT_KEY}/report/load/", LoadReportHandler).Methods("POST")
	router.HandleFunc("/api/{CLIENT_KEY}/report/process/", ProcessReportHandler).Methods("POST")
	router.HandleFunc("/api/{CLIENT_KEY}/report/runtime/", RuntimeReportHandler).Methods("POST")
	router.HandleFunc("/api/{CLIENT_KEY}/create/", ClientCreateHandler).Methods("POST")

	s.SetKeepAlivesEnabled(true)

	log.Printf("Server [PID: %d] listen on [%s]\n", os.Getpid(), "0.0.0.0:9898")
	log.Fatal(s.ListenAndServe())
}
