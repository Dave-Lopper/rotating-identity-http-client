package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"time"

	goControlTor "github.com/gwitmond/gocontroltor"
	"golang.org/x/net/proxy"
)

type TorHttpClient struct {
	RequestsTillRotation int64

	currentIp            string
	currentRequestsCount int64
	httpClient           http.Client
	proxyAddress         string
	torController        goControlTor.TorControl
	userAgent            string
}

func getCurrentIp(httpClient http.Client) (*string, error) {
	response, err := httpClient.Get("https://api.ipify.org")
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil, err
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		return nil, err
	}
	stringBody := string(body)
	return &stringBody, nil
}

func (c *TorHttpClient) Get(
	targetUrl string,
	headers map[string]string,
	params map[string]string,
) (*http.Response, error) {
	if c.currentRequestsCount >= c.RequestsTillRotation {
		fmt.Println("Needs IP rotation")
		err := c.rotateIdentity()
		if err != nil {
			return nil, err
		}
	}

	if len(params) > 0 {
		queryParams := url.Values{}
		for key, value := range params {
			queryParams.Add(key, value)
		}
		queryString := queryParams.Encode()
		targetUrl = targetUrl + "?" + queryString
	}

	request, err := http.NewRequest("GET", targetUrl, nil)
	if err != nil {
		fmt.Println("Error while instantiating request:", err)
		return nil, err
	}

	for key, value := range headers {
		request.Header.Add(key, value)
	}

	_, exists := headers["User-Agent"]
	if !exists {
		request.Header.Add("User-Agent", c.userAgent)
	}

	fmt.Printf("About to get %s with IP %s\n", targetUrl, c.currentIp)
	response, err := c.httpClient.Do(request)
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil, err
	}

	c.currentRequestsCount++
	return response, nil
}

func (c *TorHttpClient) Post(targetUrl string, headers map[string]string, body io.Reader) (*http.Response, error) {
	if c.currentRequestsCount >= c.RequestsTillRotation {
		fmt.Println("Needs IP rotation")
		err := c.rotateIdentity()
		if err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequest("POST", targetUrl, body)
	if err != nil {
		fmt.Println("Error while instantiating request:", err)
		return nil, err
	}

	for key, value := range headers {
		request.Header.Add(key, value)
	}

	_, exists := headers["User-Agent"]
	if !exists {
		request.Header.Add("User-Agent", c.userAgent)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil, err
	}
	c.currentRequestsCount++
	return response, nil
}

func (c *TorHttpClient) sendNewNymSignal() error {
	code, resp, err := c.torController.SendCommand("SIGNAL NEWNYM\r\n", 250)
	if err != nil {
		fmt.Println("Error sending NEWNYM signal:", err, resp, code)
		return err
	}
	return nil
}

func (c *TorHttpClient) renewConnection() error {
	dialer, err := proxy.SOCKS5("tcp", c.proxyAddress, nil, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
	}
	c.httpClient = http.Client{
		Transport: transport,
	}
	return nil
}

func (c *TorHttpClient) rotateIdentity() error {
	fmt.Printf("Starting rotate IP with IP: %s\n", c.currentIp)
	c.userAgent = randomUserAgent()
	err := c.sendNewNymSignal()
	fmt.Println("Sent NEWSYM signal to Tor controller, sleeping 10s before checking IP...")
	if err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	c.renewConnection()
	currentIp, err := getCurrentIp(c.httpClient)
	fmt.Printf("Refreshed IP: %s\n", *currentIp)
	if err != nil {
		return err
	}

	if *currentIp != c.currentIp {
		fmt.Println("IP rotated succesfully on attempt 1")
		c.currentIp = *currentIp
		return nil
	}

	baseDelay := 8

	for attempt := 0; attempt < 5; attempt++ {
		delay := float64(baseDelay) * math.Pow(2, float64(attempt))
		jitter := rand.Float64() * 0.1 * delay
		sleepTime := delay + jitter
		sleepDuration := time.Duration(sleepTime * float64(time.Second))

		fmt.Printf("About to sleep %f for attempt %d at rotating IP\n", sleepDuration.Seconds(), attempt)
		time.Sleep(sleepDuration)

		currentIp, err := getCurrentIp(c.httpClient)
		fmt.Printf("Refreshed IP: %s\n", *currentIp)
		if err != nil {
			return err
		}
		if *currentIp != c.currentIp {
			fmt.Printf("IP rotated succesfully on attempt %d\n", attempt)
			c.currentIp = *currentIp
			return nil
		}

		c.sendNewNymSignal()
		fmt.Println("Sent NEWSYM signal to Tor controller, sleeping 7s before checking IP...")
		time.Sleep(10 * time.Second)
		currentIp, err = getCurrentIp(c.httpClient)
		fmt.Printf("Refreshed IP: %s\n", *currentIp)
		if err != nil {
			return err
		}

		if *currentIp != c.currentIp {
			fmt.Printf("IP rotated succesfully on attempt %d\n", attempt)
			c.currentIp = *currentIp
			return nil
		}
	}

	return errors.New("couldn't rotate IP after 5 retries")
}

func NewTorHttpClient(controlAddress string, controlPassword string, proxyAddress string, requestsTillRotation int64) (*TorHttpClient, error) {
	torController := &goControlTor.TorControl{}
	err := torController.Dial("tcp", controlAddress)
	if err != nil {
		return nil, err
	}

	err = torController.PasswordAuthenticate(controlPassword)
	if err != nil {
		return nil, err
	}

	dialer, err := proxy.SOCKS5("tcp", proxyAddress, nil, nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
	}
	httpClient := &http.Client{
		Transport: transport,
	}

	currentIp, err := getCurrentIp(*httpClient)
	if err != nil {
		return nil, err
	}

	return &TorHttpClient{
		RequestsTillRotation: requestsTillRotation,
		httpClient:           *httpClient,
		currentIp:            *currentIp,
		currentRequestsCount: 0,
		proxyAddress:         proxyAddress,
		torController:        *torController,
		userAgent:            randomUserAgent(),
	}, nil
}
