package platform

import "os"

const DefaultCloudBaseURL = "https://zgsm.sangfor.com"

const InvokerEnvKey = "CSC_CLOUD_INVOKER"

func Invoker() string {
	return os.Getenv(InvokerEnvKey)
}

func IsInvokedByCsc() bool {
	return os.Getenv(InvokerEnvKey) == "csc"
}

func Getenv(key string) string {
	return os.Getenv(key)
}
