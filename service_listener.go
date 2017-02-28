package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/route53"

	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/rest"
)

// Don't actually commit the changes to route53 records, just print out what we would have done.
var dryRun bool

func init() {
	dryRunStr := os.Getenv("DRY_RUN")
	if dryRunStr != "" {
		dryRun = true
	}
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Route53 Update Service")

	clientset, err := getClientset()
	if err != nil {
		// we only support in-cluster mode currently
		panic(fmt.Sprintf("failed to connect to kubernetes using on-cluster config: %s", err))
	}

	metadata := ec2metadata.New(session.New())

	creds := credentials.NewChainCredentials(
		[]credentials.Provider{
			&credentials.EnvProvider{},
			&credentials.SharedCredentialsProvider{},
			&ec2rolecreds.EC2RoleProvider{Client: metadata},
		})

	region, err := metadata.Region()
	if err != nil {
		panic(fmt.Sprintf("Unable to retrieve the region from the EC2 instance %v\n", err))
	}

	awsConfig := aws.NewConfig()
	awsConfig.WithCredentials(creds)
	awsConfig.WithRegion(region)
	sess := session.New(awsConfig)

	r53Api := route53.New(sess)
	elbAPI := elb.New(sess)
	if r53Api == nil || elbAPI == nil {
		panic("Failed to make AWS connection")
	}

	selector := "dns=route53"
	l, err := labels.Parse(selector)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse selector %q: %v", selector, err))
	}
	listOptions := api.ListOptions{
		LabelSelector: l,
	}

	log.Println("Starting Service Polling every 30s")
	awsCallFailed := false
	for {
		if awsCallFailed {
			log.Println("Noticed failed calls to AWS services, refreshing creds")
			sess.Config.Credentials.Expire()
			awsCallFailed = false
		}

		services, err := clientset.Services(api.NamespaceAll).List(listOptions)
		if err != nil {
			panic(fmt.Sprintf("Failed to list pods: %v", err))
		}

		log.Printf("Found %d DNS services in all namespaces with selector %q\n", len(services.Items), selector)
		for i := range services.Items {
			s := &services.Items[i]
			hn, err := serviceHostname(s)
			if err != nil {
				log.Println("warning! Couldn't find hostname for", s.Name, err)
				continue
			}

			annotation, ok := s.ObjectMeta.Annotations["domainName"]
			if !ok {
				log.Println("warning! Domain name not set for", s.Name)
				continue
			}

			domains := strings.Split(annotation, ",")
			for j := range domains {
				domain := domains[j]

				log.Printf("Creating DNS for %s service: %s -> %s\n", s.Name, hn, domain)
				elbZoneID, err := hostedZoneID(elbAPI, hn)
				if err != nil {
					log.Println("warning! Couldn't get zone ID:", err)
					awsCallFailed = true
					continue
				}

				zone, err := getDestinationZone(domain, r53Api)
				if err != nil {
					log.Println("warning! Couldn't find destination zone:", err)
					awsCallFailed = true
					continue
				}

				zoneID := *zone.Id
				zoneParts := strings.Split(zoneID, "/")
				zoneID = zoneParts[len(zoneParts)-1]

				if err = updateDNS(r53Api, hn, elbZoneID, strings.TrimLeft(domain, "."), zoneID); err != nil {
					log.Println("warning!", err)
					awsCallFailed = true
					continue
				}
				log.Printf("Created dns record set: domain=%s, zoneID=%s\n", domain, zoneID)
			}
		}
		time.Sleep(30 * time.Second)
	}
}

func getClientset() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return clientset, nil
}

func getDestinationZone(domain string, r53Api *route53.Route53) (*route53.HostedZone, error) {
	tld, err := getTLD(domain)
	if err != nil {
		return nil, err
	}

	listHostedZoneInput := route53.ListHostedZonesByNameInput{
		DNSName: &tld,
	}
	hzOut, err := r53Api.ListHostedZonesByName(&listHostedZoneInput)
	if err != nil {
		return nil, fmt.Errorf("No zone found for %s: %v", tld, err)
	}
	// TODO: The AWS API may return multiple pages, we should parse them all

	return findMostSpecificZoneForDomain(domain, hzOut.HostedZones)
}

func findMostSpecificZoneForDomain(domain string, zones []*route53.HostedZone) (*route53.HostedZone, error) {
	domain = domainWithTrailingDot(domain)
	if len(zones) < 1 {
		return nil, fmt.Errorf("No zone found for %s", domain)
	}
	var mostSpecific *route53.HostedZone
	curLen := 0

	for i := range zones {
		zone := zones[i]
		zoneName := *zone.Name

		if strings.HasSuffix(domain, zoneName) && curLen < len(zoneName) {
			curLen = len(zoneName)
			mostSpecific = zone
		}
	}

	if mostSpecific == nil {
		return nil, fmt.Errorf("Zone found %s does not match domain given %s", *zones[0].Name, domain)
	}

	return mostSpecific, nil
}

func getTLD(domain string) (string, error) {
	domainParts := strings.Split(domain, ".")
	segments := len(domainParts)
	if segments < 3 {
		return "", fmt.Errorf("Domain %s is invalid - it should be a fully qualified domain name and subdomain (i.e. test.example.com)", domain)
	}
	return strings.Join(domainParts[segments-2:], "."), nil
}

func domainWithTrailingDot(withoutDot string) string {
	if withoutDot[len(withoutDot)-1:] == "." {
		return withoutDot
	}
	return fmt.Sprint(withoutDot, ".")
}

func serviceHostname(service *v1.Service) (string, error) {
	ingress := service.Status.LoadBalancer.Ingress
	if len(ingress) < 1 {
		return "", errors.New("No ingress defined for ELB")
	}
	if len(ingress) > 1 {
		return "", errors.New("Multiple ingress points found for ELB, not supported")
	}
	return ingress[0].Hostname, nil
}

func loadBalancerNameFromHostname(hostname string) (string, error) {
	var name string
	hostnameSegments := strings.Split(hostname, "-")
	if len(hostnameSegments) < 2 {
		return name, fmt.Errorf("%s is not a valid ELB hostname", hostname)
	}
	name = hostnameSegments[0]

	// handle internal load balancer naming
	if name == "internal" {
		name = hostnameSegments[1]
	}

	return name, nil
}

func hostedZoneID(elbAPI *elb.ELB, hostname string) (string, error) {
	elbName, err := loadBalancerNameFromHostname(hostname)
	if err != nil {
		return "", fmt.Errorf("Couldn't parse ELB hostname: %v", err)
	}
	lbInput := &elb.DescribeLoadBalancersInput{
		LoadBalancerNames: []*string{
			&elbName,
		},
	}
	resp, err := elbAPI.DescribeLoadBalancers(lbInput)
	if err != nil {
		return "", fmt.Errorf("Could not describe load balancer: %v", err)
	}
	descs := resp.LoadBalancerDescriptions
	if len(descs) < 1 {
		return "", fmt.Errorf("No lb found: %v", err)
	}
	if len(descs) > 1 {
		return "", fmt.Errorf("Multiple lbs found: %v", err)
	}
	return *descs[0].CanonicalHostedZoneNameID, nil
}

func updateDNS(r53Api *route53.Route53, hn, hzID, domain, zoneID string) error {
	at := route53.AliasTarget{
		DNSName:              &hn,
		EvaluateTargetHealth: aws.Bool(false),
		HostedZoneId:         &hzID,
	}
	rrs := route53.ResourceRecordSet{
		AliasTarget: &at,
		Name:        &domain,
		Type:        aws.String("A"),
	}
	change := route53.Change{
		Action:            aws.String("UPSERT"),
		ResourceRecordSet: &rrs,
	}
	batch := route53.ChangeBatch{
		Changes: []*route53.Change{&change},
		Comment: aws.String("Kubernetes Update to Service"),
	}
	crrsInput := route53.ChangeResourceRecordSetsInput{
		ChangeBatch:  &batch,
		HostedZoneId: &zoneID,
	}
	if dryRun {
		log.Printf("DRY RUN: We normally would have updated %s to point to %s (%s)\n", zoneID, hzID, hn)
		return nil
	}

	_, err := r53Api.ChangeResourceRecordSets(&crrsInput)
	if err != nil {
		return fmt.Errorf("Failed to update record set: %v", err)
	}
	return nil
}
