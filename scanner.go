package zgrab2

import (
	"fmt"
	"log"
	"time"
)

var scanners map[string]*Scanner
var orderedScanners []string

// RegisterScan registers each individual scanner to be ran by the framework
func RegisterScan(name string, s Scanner) {
	//add to list and map
	if scanners[name] != nil {
		log.Fatalf("name: %s already used", name)
	}
	orderedScanners = append(orderedScanners, name)
	scanners[name] = &s
}

// PrintScanners prints all registered scanners
func PrintScanners() {
	for k, v := range scanners {
		fmt.Println(k, v)
	}
}

// RunScanner runs a single scan on a target and returns the resulting data
func RunScanner(s Scanner, mon *Monitor, target ScanTarget) (string, ScanResponse) {
	t := time.Now()
	res, e := s.Scan(target)
	var err *error //nil pointers are null in golang, which is not nil and not empty
	if e == nil {
		mon.statusesChan <- moduleStatus{name: s.GetName(), st: statusSuccess}
		err = nil
	} else {
		mon.statusesChan <- moduleStatus{name: s.GetName(), st: statusFailure}
		err = &e
	}
	resp := ScanResponse{Result: res, Error: err, Time: t.Format(time.RFC3339)}
	return s.GetName(), resp
}

func init() {
	scanners = make(map[string]*Scanner)
}
