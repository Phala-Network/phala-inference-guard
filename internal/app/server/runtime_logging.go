package server

import "log"

func configureRuntimeLogging() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
}
