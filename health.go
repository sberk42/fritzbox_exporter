package main

import (
    "github.com/heptiolabs/healthcheck"
    "time"
)

// createHealthChecks will create the readiness and liveness endpoints and add the check functions.
func createHealthChecks(gatewayUrl string) healthcheck.Handler {
    health := healthcheck.NewHandler()

    health.AddReadinessCheck("FRITZ!Box connection",
        healthcheck.HTTPGetCheck(gatewayUrl+"/any.xml", time.Duration(3)*time.Second))

    health.AddLivenessCheck("go-routines", healthcheck.GoroutineCountCheck(100))
    return health
}
