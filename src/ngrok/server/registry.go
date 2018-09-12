package server

import (
	"encoding/gob"
	"fmt"
	"net"
	"ngrok/cache"
	"ngrok/log"
	"sync"
	"time"
)

const (
	cacheSaveInterval time.Duration = 10 * time.Minute
)

type cacheUrl string

func (url cacheUrl) Size() int {
	return len(url)
}

// TunnelRegistry maps a tunnel URL to Tunnel structures
// TunnelRegistry将隧道URL映射到隧道结构
type TunnelRegistry struct {
	tunnels  map[string]*Tunnel //隧道记录
	affinity *cache.LRUCache //
	log.Logger
	sync.RWMutex //RWMutex(读写锁)
}

//
func NewTunnelRegistry(cacheSize uint64, cacheFile string) *TunnelRegistry {
	registry := &TunnelRegistry{
		tunnels:  make(map[string]*Tunnel),
		affinity: cache.NewLRUCache(cacheSize),
		Logger:   log.NewPrefixLogger("registry", "tun"),
	}

	// LruCache利用Gob编码。不幸的是，Gob是易变的，将无法编码或解码任何非原始类型，还没有“注册”它。因为我们店cacheurl对象，我们需要在这里先登记的编码/解码工作
	// LRUCache uses Gob encoding. Unfortunately, Gob is fickle and will fail
	// to encode or decode any non-primitive types that haven't been "registered"
	// with it. Since we store cacheUrl objects, we need to register them here first
	// for the encoding/decoding to work
	// 因此我们先存储cacheUrl对象，我们要加密解密必须先在这里注册他们
	var urlobj cacheUrl
	gob.Register(urlobj)

	// try to load and then periodically save the affinity cache to file, if specified
	if cacheFile != "" {
		err := registry.affinity.LoadItemsFromFile(cacheFile)
		if err != nil {
			registry.Error("Failed to load affinity cache %s: %v", cacheFile, err)
		}

		registry.SaveCacheThread(cacheFile, cacheSaveInterval)
	} else {
		registry.Info("No affinity cache specified")
	}

	return registry
}

// Spawns a goroutine the periodically saves the cache to a file.
func (r *TunnelRegistry) SaveCacheThread(path string, interval time.Duration) {
	go func() {
		r.Info("Saving affinity cache to %s every %s", path, interval.String())
		for {
			time.Sleep(interval)

			r.Debug("Saving affinity cache")
			err := r.affinity.SaveItemsToFile(path)
			if err != nil {
				r.Error("Failed to save affinity cache: %v", err)
			} else {
				r.Info("Saved affinity cache")
			}
		}
	}()
}

// Register a tunnel with a specific url, returns an error
// if a tunnel is already registered at that url
func (r *TunnelRegistry) Register(url string, t *Tunnel) error {
	r.Lock()
	defer r.Unlock()

	if r.tunnels[url] != nil {
		return fmt.Errorf("The tunnel %s is already registered.", url)
	}

	r.tunnels[url] = t

	return nil
}

func (r *TunnelRegistry) cacheKeys(t *Tunnel) (ip string, id string) {
	clientIp := t.ctl.conn.RemoteAddr().(*net.TCPAddr).IP.String()
	clientId := t.ctl.id

	ipKey := fmt.Sprintf("client-ip-%s:%s", t.req.Protocol, clientIp)
	idKey := fmt.Sprintf("client-id-%s:%s", t.req.Protocol, clientId)
	return ipKey, idKey
}

func (r *TunnelRegistry) GetCachedRegistration(t *Tunnel) (url string) {
	ipCacheKey, idCacheKey := r.cacheKeys(t)

	// check cache for ID first, because we prefer that over IP which might
	// not be specific to a user because of NATs
	if v, ok := r.affinity.Get(idCacheKey); ok {
		url = string(v.(cacheUrl))
		t.Debug("Found registry affinity %s for %s", url, idCacheKey)
	} else if v, ok := r.affinity.Get(ipCacheKey); ok {
		url = string(v.(cacheUrl))
		t.Debug("Found registry affinity %s for %s", url, ipCacheKey)
	}
	return
}

func (r *TunnelRegistry) RegisterAndCache(url string, t *Tunnel) (err error) {
	if err = r.Register(url, t); err == nil {
		// we successfully assigned a url, cache it
		ipCacheKey, idCacheKey := r.cacheKeys(t)
		r.affinity.Set(ipCacheKey, cacheUrl(url))
		r.affinity.Set(idCacheKey, cacheUrl(url))
	}
	return

}

// Register a tunnel with the following process:
// Consult the affinity cache to try to assign a previously used tunnel url if possible
// Generate new urls repeatedly with the urlFn and register until one is available.
// 使用以下过程注册隧道:
// Consult 亲和缓存尝试分配以前使用的隧道 url, 如果 possible
// Generate 新的 url 重复使用 urlFn 和注册, 直到一个可用
// !!! 使用一个随机数注册，如果该随机数已被占用，再重新换一个随机数再试，最多试5次。
func (r *TunnelRegistry) RegisterRepeat(urlFn func() string, t *Tunnel) (string, error) {
	url := r.GetCachedRegistration(t)
	if url == "" {
		url = urlFn()
	}

	maxAttempts := 5
	for i := 0; i < maxAttempts; i++ {
		if err := r.RegisterAndCache(url, t); err != nil {
			// pick a new url and try again
			url = urlFn()
		} else {
			// we successfully assigned a url, we're done
			return url, nil
		}
	}

	return "", fmt.Errorf("Failed to assign a URL after %d attempts!", maxAttempts)
}

func (r *TunnelRegistry) Del(url string) {
	r.Lock()
	defer r.Unlock()
	delete(r.tunnels, url)
}

func (r *TunnelRegistry) Get(url string) *Tunnel {
	r.RLock()
	defer r.RUnlock()
	return r.tunnels[url]
}

// ControlRegistry maps a client ID to Control structures
// ControlRegistry 映射客户端 ID 以控制结构
type ControlRegistry struct {
	controls map[string]*Control
	log.Logger
	sync.RWMutex
}

func NewControlRegistry() *ControlRegistry {
	return &ControlRegistry{
		controls: make(map[string]*Control),
		Logger:   log.NewPrefixLogger("registry", "ctl"),
	}
}

func (r *ControlRegistry) Get(clientId string) *Control {
	r.RLock()
	defer r.RUnlock()
	return r.controls[clientId]
}

func (r *ControlRegistry) Add(clientId string, ctl *Control) (oldCtl *Control) {
	r.Lock()
	defer r.Unlock()

	//如果控制器已存在，则用新的替换原来的
	oldCtl = r.controls[clientId]
	if oldCtl != nil {
		oldCtl.Replaced(ctl)
	}

	r.controls[clientId] = ctl
	r.Info("Registered control with id %s", clientId)
	return
}

func (r *ControlRegistry) Del(clientId string) error {
	r.Lock()
	defer r.Unlock()
	if r.controls[clientId] == nil {
		return fmt.Errorf("No control found for client id: %s", clientId)
	} else {
		r.Info("Removed control registry id %s", clientId)
		delete(r.controls, clientId)
		return nil
	}
}
