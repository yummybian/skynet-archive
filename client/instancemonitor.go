package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/bketelsen/skynet"
	"github.com/bketelsen/skynet/service"
	"path"
)

type InstanceMonitor struct {
	doozer           *skynet.DoozerConnection
	clients          map[string]*InstanceListener
	listChan         chan *InstanceListener
	instances        map[string]service.Service
	notificationChan chan InstanceMonitorNotification
}

func NewInstanceMonitor(doozer *skynet.DoozerConnection) (im *InstanceMonitor) {
	im = &InstanceMonitor{
		doozer:           doozer,
		clients:          make(map[string]*InstanceListener, 0),
		notificationChan: make(chan InstanceMonitorNotification),
		listChan:         make(chan *InstanceListener),
		instances:        make(map[string]service.Service, 0),
	}

	go im.mux()
	go im.monitorInstances()

	return
}

type InstanceMonitorNotification struct {
	Path    string
	Service service.Service
	Type    InstanceNotificationType
}

type InstanceNotificationType int

const (
	InstanceAddNotification = iota
	InstanceUpdateNotification
	InstanceRemoveNotification
)

func (im *InstanceMonitor) mux() {
	for {
		select {
		case notification := <-im.notificationChan:

			// Update internal instance list
			switch notification.Type {
			case InstanceAddNotification, InstanceUpdateNotification:
				im.instances[notification.Path] = notification.Service
			case InstanceRemoveNotification:
				delete(im.instances, notification.Path)
			}

			for _, c := range im.clients {
				if c.query.PathMatches(notification.Path) {
					go c.notify(notification)
				}
			}

		case listener := <-im.listChan:
			for path, s := range im.instances {
				if listener.query.PathMatches(path) {
					listener.Instances[path] = s
				}
			}

			listener.doneChan <- true
		}
	}
}

func (im *InstanceMonitor) RemoveListener(id string) {
	delete(im.clients, id)
}

func (im *InstanceMonitor) monitorInstances() {
	rev := im.doozer.GetCurrentRevision()

	// Build initial list of instances
	var ifc instanceFileCollector
	errch := make(chan error)
	im.doozer.Walk(rev, "/services", &ifc, errch)

	select {
	case err := <-errch:
		fmt.Println(err)
	default:
	}

	for _, file := range ifc.files {
		buf, _, err := im.doozer.Get(file, rev)
		if err != nil {
			fmt.Println(err)
			continue
		}

		var s service.Service
		err = json.Unmarshal(buf, &s)
		if err != nil {
			fmt.Println("error unmarshalling service")
			continue
		}

		im.instances[file] = s
	}

	// Watch for changes

	watchPath := path.Join("/services", "**")

	for {
		ev, err := im.doozer.Wait(watchPath, rev+1)
		rev = ev.Rev

		var s service.Service

		if err != nil {
			continue
		}

		if ev.IsDel() {
			im.notificationChan <- InstanceMonitorNotification{
				Path:    ev.Path,
				Service: im.instances[ev.Path],
				Type:    InstanceRemoveNotification,
			}
		} else {
			buf := bytes.NewBuffer(ev.Body)

			err = json.Unmarshal(buf.Bytes(), &s)

			if err != nil {
				fmt.Println("error unmarshalling service")
				continue
			}

			var notificationType InstanceNotificationType = InstanceAddNotification

			if _, ok := im.instances[ev.Path]; ok {
				notificationType = InstanceUpdateNotification
			}

			im.notificationChan <- InstanceMonitorNotification{
				Path:    ev.Path,
				Service: s,
				Type:    notificationType,
			}
		}
	}

}

func (im *InstanceMonitor) Listen(id string, q *Query) (l *InstanceListener) {
	l = NewInstanceListener(im, id, q)

	im.listChan <- l
	<-l.doneChan

	im.clients[id] = l

	return
}
