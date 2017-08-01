package main

import (
	"github.com/jessevdk/go-flags"
	log "github.com/sirupsen/logrus"
	"github.com/zmap/zgrab2/zgrab2"
	_ "github.com/zmap/zgrab2/zmodules"
	"os"
)

func main() {
	if _, err := zgrab2.Parser.Parse(); err != nil {
		if flagsErr, ok := err.(*flags.Error); ok {
			// If flag parsed and flag is Help type, exit 0
			if flagsErr.Type == flags.ErrHelp {
				os.Exit(0)
			}
		} else {
			log.Fatal(err.Error())
		}
	}

	//m := zgrab2.MakeMonitor()
	//zgrab2.Process(os.Stdout, m)
}
