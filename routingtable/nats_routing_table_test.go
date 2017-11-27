package routingtable_test

import (
	"fmt"

	mfakes "code.cloudfoundry.org/diego-logging-client/testhelpers"

	"code.cloudfoundry.org/bbs/models"
	"code.cloudfoundry.org/lager/lagertest"
	"code.cloudfoundry.org/route-emitter/routingtable"
	. "code.cloudfoundry.org/route-emitter/routingtable/matchers"
	"code.cloudfoundry.org/routing-info/cfroutes"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
)

var _ = Describe("RoutingTable", func() {
	var (
		table            routingtable.RoutingTable
		messagesToEmit   routingtable.MessagesToEmit
		logger           *lagertest.TestLogger
		fakeMetronClient *mfakes.FakeIngressClient
	)

	key := routingtable.RoutingKey{ProcessGUID: "some-process-guid", ContainerPort: 8080}

	hostname1 := "foo.example.com"
	hostname2 := "bar.example.com"
	hostname3 := "baz.example.com"

	internalHostname1 := "internal-1"
	internalHostname2 := "internal-2"

	domain := "domain"

	olderTag := &models.ModificationTag{Epoch: "abc", Index: 0}
	currentTag := &models.ModificationTag{Epoch: "abc", Index: 1}
	newerTag := &models.ModificationTag{Epoch: "def", Index: 0}

	endpoint1 := routingtable.Endpoint{
		InstanceGUID:    "ig-1",
		Host:            "1.1.1.1",
		ContainerIP:     "1.2.3.4",
		Index:           0,
		Port:            11,
		ContainerPort:   8080,
		Evacuating:      false,
		ModificationTag: currentTag,
	}
	endpoint2 := routingtable.Endpoint{
		InstanceGUID:    "ig-2",
		Host:            "2.2.2.2",
		ContainerIP:     "2.3.4.5",
		Index:           1,
		Port:            22,
		ContainerPort:   8080,
		Evacuating:      false,
		ModificationTag: currentTag,
	}
	endpoint3 := routingtable.Endpoint{
		InstanceGUID:    "ig-3",
		Host:            "3.3.3.3",
		ContainerIP:     "3.4.5.6",
		Index:           2,
		Port:            33,
		ContainerPort:   8080,
		Evacuating:      false,
		ModificationTag: currentTag,
	}
	collisionEndpoint := routingtable.Endpoint{
		InstanceGUID:    "ig-4",
		Host:            "1.1.1.1",
		ContainerIP:     "1.2.3.4",
		Index:           3,
		Port:            11,
		ContainerPort:   8080,
		Evacuating:      false,
		ModificationTag: currentTag,
	}
	newInstanceEndpointAfterEvacuation := routingtable.Endpoint{
		InstanceGUID:    "ig-5",
		Host:            "5.5.5.5",
		ContainerIP:     "4.5.6.7",
		Index:           0,
		Port:            55,
		ContainerPort:   8080,
		Evacuating:      false,
		ModificationTag: currentTag,
	}
	evacuating1 := routingtable.Endpoint{
		InstanceGUID:    "ig-1",
		Host:            "1.1.1.1",
		ContainerIP:     "1.2.3.4",
		Index:           0,
		Port:            11,
		ContainerPort:   8080,
		Evacuating:      true,
		ModificationTag: currentTag,
	}

	logGuid := "some-log-guid"

	domains := models.NewDomainSet([]string{domain})
	noFreshDomains := models.NewDomainSet([]string{})

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test-route-emitter")

		fakeMetronClient = &mfakes.FakeIngressClient{}
		table = routingtable.NewRoutingTable(logger, false, fakeMetronClient)
	})

	createSchedulingInfo := func(serviceURL string) *models.DesiredLRPSchedulingInfo {
		routingInfo := cfroutes.CFRoutes{
			{
				Hostnames:       []string{hostname1, hostname2},
				Port:            key.ContainerPort,
				RouteServiceUrl: serviceURL,
			},
		}.RoutingInfo()
		routes := models.Routes{}
		for key, message := range routingInfo {
			routes[key] = message
		}

		info := models.NewDesiredLRPSchedulingInfo(models.NewDesiredLRPKey(key.ProcessGUID, "domain", logGuid), "", 3, models.NewDesiredLRPResource(0, 0, 0, ""), routes, *currentTag, nil, nil)
		return &info
	}

	createSchedulingInfoWithIS := func(isolationSegment string) *models.DesiredLRPSchedulingInfo {
		routingInfo := cfroutes.CFRoutes{
			{
				Hostnames:        []string{hostname1, hostname2},
				Port:             key.ContainerPort,
				IsolationSegment: isolationSegment,
			},
		}.RoutingInfo()
		routes := models.Routes{}
		for key, message := range routingInfo {
			routes[key] = message
		}

		info := models.NewDesiredLRPSchedulingInfo(models.NewDesiredLRPKey(key.ProcessGUID, "domain", logGuid), "", 3, models.NewDesiredLRPResource(0, 0, 0, ""), routes, *currentTag, nil, nil)
		return &info
	}

	Describe("Evacuating endpoints", func() {
		BeforeEach(func() {
			schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1)
			_, messagesToEmit = table.SetRoutes(nil, schedulingInfo)
			Expect(messagesToEmit).To(BeZero())

			actualLRP := createActualLRP(key, endpoint1, domain)
			_, messagesToEmit = table.AddEndpoint(actualLRP)
			expected := routingtable.MessagesToEmit{
				RegistrationMessages: []routingtable.RegistryMessage{
					routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
				},
			}
			Expect(messagesToEmit).To(MatchMessagesToEmit(expected))

			actualLRP = createActualLRP(key, evacuating1, domain)
			_, messagesToEmit = table.AddEndpoint(actualLRP)
			Expect(messagesToEmit).To(BeZero())

			actualLRP = createActualLRP(key, endpoint1, domain)
			_, messagesToEmit = table.RemoveEndpoint(actualLRP)
			Expect(messagesToEmit).To(BeZero())
		})

		It("does not log an address collision", func() {
			Consistently(logger).ShouldNot(Say("collision-detected-with-endpoint"))
		})

		Context("when we have an evacuating endpoint and an instance for that added", func() {
			It("emits a registration for the instance and a unregister for the evacuating", func() {
				evacuatingActualLRP := createActualLRP(key, newInstanceEndpointAfterEvacuation, domain)
				_, messagesToEmit = table.AddEndpoint(evacuatingActualLRP)
				expected := routingtable.MessagesToEmit{
					RegistrationMessages: []routingtable.RegistryMessage{
						routingtable.RegistryMessageFor(newInstanceEndpointAfterEvacuation, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
					},
				}
				Expect(messagesToEmit).To(MatchMessagesToEmit(expected))

				actualLRP := createActualLRP(key, evacuating1, domain)
				_, messagesToEmit = table.RemoveEndpoint(actualLRP)
				expected = routingtable.MessagesToEmit{
					UnregistrationMessages: []routingtable.RegistryMessage{
						routingtable.RegistryMessageFor(evacuating1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
					},
				}
				Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
			})
		})
	})

	Context("when internal address message builder is used", func() {
		BeforeEach(func() {
			table = routingtable.NewRoutingTable(logger, true, fakeMetronClient)
			desiredLRP := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1)
			table.SetRoutes(nil, desiredLRP)
		})

		Context("and an endpoint is added", func() {
			var (
				actualLRP *routingtable.ActualLRPRoutingInfo
			)

			BeforeEach(func() {
				actualLRP = createActualLRP(key, endpoint1, domain)
				_, messagesToEmit = table.AddEndpoint(actualLRP)
			})

			It("emits the container ip and port instead of the host ip and port", func() {
				expected := routingtable.MessagesToEmit{
					RegistrationMessages: []routingtable.RegistryMessage{
						{
							URIs:             []string{hostname1},
							Host:             "1.2.3.4",
							Port:             8080,
							App:              logGuid,
							IsolationSegment: "",
							Tags:             map[string]string{"component": "route-emitter"},

							ServerCertDomainSAN:  "ig-1",
							PrivateInstanceId:    "ig-1",
							PrivateInstanceIndex: "0",
							RouteServiceUrl:      "",
						},
					},
				}
				Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
			})

			Context("then the endpoint is removed", func() {
				BeforeEach(func() {
					_, messagesToEmit = table.RemoveEndpoint(actualLRP)
				})

				It("emits the container ip and port", func() {
					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							{
								URIs:             []string{hostname1},
								Host:             "1.2.3.4",
								Port:             8080,
								App:              logGuid,
								IsolationSegment: "",
								Tags:             map[string]string{"component": "route-emitter"},

								ServerCertDomainSAN:  "ig-1",
								PrivateInstanceId:    "ig-1",
								PrivateInstanceIndex: "0",
								RouteServiceUrl:      "",
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})
		})
	})

	Describe("Swap", func() {
		Context("when we have existing stuff in the table and an unfresh domain", func() {
			BeforeEach(func() {
				tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)

				routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
				schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
				lrp := createActualLRP(key, endpoint1, domain)
				tempTable.SetRoutes(nil, schedulingInfo)
				tempTable.AddEndpoint(lrp)

				table.Swap(tempTable, domains)

				tempTable = routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				routes = createRoutingInfo(key.ContainerPort, []string{hostname1, hostname3}, []string{internalHostname2}, "", []uint32{}, "")
				schedulingInfo = createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
				tempTable = routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				tempTable.SetRoutes(nil, schedulingInfo)
				tempTable.AddEndpoint(lrp)

				_, messagesToEmit = table.Swap(tempTable, noFreshDomains)
			})

			It("emits only additive changes", func() {
				expected := routingtable.MessagesToEmit{
					RegistrationMessages: []routingtable.RegistryMessage{
						routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
					},
					InternalRegistrationMessages: []routingtable.RegistryMessage{
						{
							Host: endpoint1.ContainerIP,
							URIs: []string{internalHostname2, fmt.Sprintf("%d.%s", 0, internalHostname2)},
							Tags: map[string]string{
								"component": "route-emitter",
							},
							PrivateInstanceIndex: "0",
							App:                  logGuid,
						},
					},
				}
				Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
			})

			Context("subsequent swaps with still not fresh domain", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1, hostname3)
					lrp := createActualLRP(key, endpoint1, domain)
					tempTable.SetRoutes(nil, schedulingInfo)
					tempTable.AddEndpoint(lrp)

					_, messagesToEmit = table.Swap(tempTable, noFreshDomains)
				})

				It("emits nothing", func() {
					Expect(messagesToEmit.RegistrationMessages).To(BeEmpty())
					Expect(messagesToEmit.UnregistrationMessages).To(BeEmpty())
				})
			})

			Context("subsequent swaps with fresh", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1, hostname3)
					lrp := createActualLRP(key, endpoint1, domain)
					tempTable.SetRoutes(nil, schedulingInfo)
					tempTable.AddEndpoint(lrp)
					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits unregisters the old route", func() {
					expected := []routingtable.RegistryMessage{
						routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
					}

					Expect(messagesToEmit.UnregistrationMessages).To(Equal(expected))
					Expect(messagesToEmit.RegistrationMessages).To(BeEmpty())
				})
			})
		})

		Context("when a new routing key arrives", func() {
			Context("when the routing key has both routes and endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)

					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					lrp1 := createActualLRP(key, endpoint1, domain)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.SetRoutes(nil, schedulingInfo)
					tempTable.AddEndpoint(lrp1)
					tempTable.AddEndpoint(lrp2)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits registrations for each pairing", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalRegistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the process only has routes", func() {
				var schedulingInfo *models.DesiredLRPSchedulingInfo
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo = createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("should not emit a registration", func() {
					Expect(messagesToEmit).To(BeZero())
				})

				Context("when the endpoints subsequently arrive", func() {
					BeforeEach(func() {
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						lrp := createActualLRP(key, endpoint1, domain)
						tempTable.SetRoutes(nil, schedulingInfo)
						tempTable.AddEndpoint(lrp)

						_, messagesToEmit = table.Swap(tempTable, domains)
					})

					It("emits registrations for each pairing", func() {
						expected := routingtable.MessagesToEmit{
							RegistrationMessages: []routingtable.RegistryMessage{
								routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							},
							InternalRegistrationMessages: []routingtable.RegistryMessage{
								{
									Host:                 endpoint1.ContainerIP,
									URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
									PrivateInstanceIndex: "0",
									App:                  logGuid,
									Tags: map[string]string{
										"component": "route-emitter",
									},
								},
							},
						}
						Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
					})
				})

				Context("when the routing key subsequently disappears", func() {
					BeforeEach(func() {
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						_, messagesToEmit = table.Swap(tempTable, domains)
					})

					It("emits nothing", func() {
						Expect(messagesToEmit).To(BeZero())
					})
				})
			})

			Context("when the process only has endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					lrp := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("should not emit a registration", func() {
					Expect(messagesToEmit).To(BeZero())
				})

				Context("when the routes subsequently arrive", func() {
					BeforeEach(func() {
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{internalHostname1}, "", []uint32{}, "")
						schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
						lrp := createActualLRP(key, endpoint1, domain)
						tempTable.SetRoutes(nil, schedulingInfo)
						tempTable.AddEndpoint(lrp)

						_, messagesToEmit = table.Swap(tempTable, domains)
					})

					It("emits registrations for each pairing", func() {
						expected := routingtable.MessagesToEmit{
							RegistrationMessages: []routingtable.RegistryMessage{
								routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							},
							InternalRegistrationMessages: []routingtable.RegistryMessage{
								{
									Host:                 endpoint1.ContainerIP,
									URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
									PrivateInstanceIndex: "0",
									App:                  logGuid,
									Tags: map[string]string{
										"component": "route-emitter",
									},
								},
							},
						}
						Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
					})
				})

				Context("when the endpoint subsequently disappears", func() {
					BeforeEach(func() {
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						_, messagesToEmit = table.Swap(tempTable, domains)
					})

					It("emits nothing", func() {
						Expect(messagesToEmit).To(BeZero())
					})
				})
			})
		})

		Context("when there is an existing routing key with an isolation segment", func() {
			var (
				schedulingInfo *models.DesiredLRPSchedulingInfo
			)

			BeforeEach(func() {
				tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				schedulingInfo = createSchedulingInfoWithIS("isolation-segment-1")
				tempTable.SetRoutes(nil, schedulingInfo)
				lrp := createActualLRP(key, endpoint1, domain)
				tempTable.AddEndpoint(lrp)
				table.Swap(tempTable, domains)
			})

			Context("when the isolation segment changes in an event", func() {
				BeforeEach(func() {
					afterSchedulingInfo := createSchedulingInfoWithIS("isolation-segment-2")
					afterSchedulingInfo.ModificationTag.Index++
					_, messagesToEmit = table.SetRoutes(schedulingInfo, afterSchedulingInfo)
				})

				It("emits a registration and unregistration", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, IsolationSegment: "isolation-segment-2"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, IsolationSegment: "isolation-segment-2"}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, IsolationSegment: "isolation-segment-1"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, IsolationSegment: "isolation-segment-1"}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the isolation segment changes in sync", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					schedulingInfo := createSchedulingInfoWithIS("isolation-segment-2")
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp)
					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits all registrations and no unregistration", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, IsolationSegment: "isolation-segment-2"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, IsolationSegment: "isolation-segment-2"}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, IsolationSegment: "isolation-segment-1"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, IsolationSegment: "isolation-segment-1"}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})
		})

		Context("when there is an existing routing key with a route service url", func() {
			var (
				schedulingInfo *models.DesiredLRPSchedulingInfo
			)

			BeforeEach(func() {
				tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				schedulingInfo = createSchedulingInfo("https://rs.example.com")
				tempTable.SetRoutes(nil, schedulingInfo)
				lrp := createActualLRP(key, endpoint1, domain)
				tempTable.AddEndpoint(lrp)
				table.Swap(tempTable, domains)
			})

			Context("when the route service url changes in an event", func() {
				BeforeEach(func() {
					afterSchedulingLRP := createSchedulingInfo("https://rs.new.example.com")
					afterSchedulingLRP.ModificationTag.Index++
					_, messagesToEmit = table.SetRoutes(schedulingInfo, afterSchedulingLRP)
				})

				It("emits all registrations and no unregistration", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.new.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.new.example.com"}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the route service url changes during sync", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					schedulingInfo := createSchedulingInfo("https://rs.new.example.com")
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits all registrations and no unregistration", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.new.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.new.example.com"}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})
		})

		Context("when the routing key has an evacuating and instance endpoint", func() {
			BeforeEach(func() {
				tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
				schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
				tempTable.SetRoutes(nil, schedulingInfo)
				evacuating := createActualLRP(key, evacuating1, domain)
				tempTable.AddEndpoint(evacuating)
				lrp2 := createActualLRP(key, endpoint2, domain)
				tempTable.AddEndpoint(lrp2)

				_, messagesToEmit = table.Swap(tempTable, domains)
			})

			It("should not emit an unregistration ", func() {
				expected := routingtable.MessagesToEmit{
					RegistrationMessages: []routingtable.RegistryMessage{
						routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
						routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						routingtable.RegistryMessageFor(evacuating1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
						routingtable.RegistryMessageFor(evacuating1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
					},
					InternalRegistrationMessages: []routingtable.RegistryMessage{
						{
							Host:                 endpoint2.ContainerIP,
							URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
							PrivateInstanceIndex: "1",
							App:                  logGuid,
							Tags: map[string]string{
								"component": "route-emitter",
							},
						},
						{
							Host:                 evacuating1.ContainerIP,
							URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
							PrivateInstanceIndex: "0",
							App:                  logGuid,
							Tags: map[string]string{
								"component": "route-emitter",
							},
						},
					},
				}
				Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
			})
		})

		Context("when there is an existing routing key", func() {
			BeforeEach(func() {
				tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
				schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
				tempTable.SetRoutes(nil, schedulingInfo)
				lrp1 := createActualLRP(key, endpoint1, domain)
				tempTable.AddEndpoint(lrp1)
				lrp2 := createActualLRP(key, endpoint2, domain)
				tempTable.AddEndpoint(lrp2)

				table.Swap(tempTable, domains)
			})

			Context("when nothing changes", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits nothing", func() {
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when the routing key gets new routes", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2, hostname3}, []string{internalHostname1, internalHostname2}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits only the new route", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
						},
						InternalRegistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 1, internalHostname2)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 0, internalHostname2)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key without any route service url gets routes with a new route service url", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "https://rs.example.com", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits registrations and unregistration", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: ""}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: ""}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: ""}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: ""}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key gets new endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)
					lrp3 := createActualLRP(key, endpoint3, domain)
					tempTable.AddEndpoint(lrp3)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits only the new registrations and no unregistration", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalRegistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint3.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 2, internalHostname1)},
								PrivateInstanceIndex: "2",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key gets a new evacuating endpoint", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)
					evacuating := createActualLRP(key, evacuating1, domain)
					tempTable.AddEndpoint(evacuating)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits no unregistration", func() {
					Expect(messagesToEmit).To(BeZero())
				})

				Context("when running instance is removed", func() {
					BeforeEach(func() {
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
						schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
						tempTable.SetRoutes(nil, schedulingInfo)
						lrp2 := createActualLRP(key, endpoint2, domain)
						tempTable.AddEndpoint(lrp2)
						evacuating := createActualLRP(key, evacuating1, domain)
						tempTable.AddEndpoint(evacuating)

						_, messagesToEmit = table.Swap(tempTable, domains)
					})

					It("emits no unregistration", func() {
						Expect(messagesToEmit).To(BeZero())
					})
				})
			})

			Context("when the routing key gets new routes and endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2, hostname3}, []string{internalHostname1, internalHostname2}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)
					lrp3 := createActualLRP(key, endpoint3, domain)
					tempTable.AddEndpoint(lrp3)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits the relevant registrations and no unregisration", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalRegistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint3.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 2, internalHostname1)},
								PrivateInstanceIndex: "2",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint3.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 2, internalHostname2)},
								PrivateInstanceIndex: "2",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 1, internalHostname2)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 0, internalHostname2)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key loses routes", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits the relevant unregistrations", func() {
					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key loses endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits the relevant unregistrations", func() {
					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key loses http/internal routes and endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits no registrations and the relevant unregisrations", func() {
					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key gains routes but loses endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2, hostname3}, []string{internalHostname1, internalHostname2}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits the relevant registrations and the relevant unregisrations", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalRegistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 0, internalHostname2)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key loses routes but gains endpoints", func() {
				BeforeEach(func() {
					tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					tempTable.SetRoutes(nil, schedulingInfo)
					lrp1 := createActualLRP(key, endpoint1, domain)
					tempTable.AddEndpoint(lrp1)
					lrp2 := createActualLRP(key, endpoint2, domain)
					tempTable.AddEndpoint(lrp2)
					lrp3 := createActualLRP(key, endpoint3, domain)
					tempTable.AddEndpoint(lrp3)

					_, messagesToEmit = table.Swap(tempTable, domains)
				})

				It("emits the relevant registrations and the relevant unregisrations", func() {
					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when the routing key disappears entirely", func() {
				var tempTable routingtable.RoutingTable
				var domainSet models.DomainSet

				BeforeEach(func() {
					tempTable = routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				})

				JustBeforeEach(func() {
					_, messagesToEmit = table.Swap(tempTable, domainSet)
				})

				Context("when the domain is fresh", func() {
					BeforeEach(func() {
						domainSet = domains
					})

					It("should unregister the missing guids", func() {
						expected := routingtable.MessagesToEmit{
							UnregistrationMessages: []routingtable.RegistryMessage{
								routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
								routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
								routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
								routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							},
							InternalUnregistrationMessages: []routingtable.RegistryMessage{
								{
									Host:                 endpoint2.ContainerIP,
									URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
									PrivateInstanceIndex: "1",
									App:                  logGuid,
									Tags: map[string]string{
										"component": "route-emitter",
									},
								},
								{
									Host:                 endpoint1.ContainerIP,
									URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
									PrivateInstanceIndex: "0",
									App:                  logGuid,
									Tags: map[string]string{
										"component": "route-emitter",
									},
								},
							},
						}
						Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
					})
				})

				Context("when the domain is not fresh", func() {
					BeforeEach(func() {
						domainSet = noFreshDomains
					})

					It("should unregister the missing guids", func() {
						expected := routingtable.MessagesToEmit{
							UnregistrationMessages: []routingtable.RegistryMessage{
								routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
								routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
								routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
								routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							},
							InternalUnregistrationMessages: []routingtable.RegistryMessage{
								{
									Host:                 endpoint2.ContainerIP,
									URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
									PrivateInstanceIndex: "1",
									App:                  logGuid,
									Tags: map[string]string{
										"component": "route-emitter",
									},
								},
								{
									Host:                 endpoint1.ContainerIP,
									URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
									PrivateInstanceIndex: "0",
									App:                  logGuid,
									Tags: map[string]string{
										"component": "route-emitter",
									},
								},
							},
						}
						Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
					})
				})

				Context("when the table is repeatedly swapped", func() {
					JustBeforeEach(func() {
						lrp1 := createActualLRP(key, endpoint1, domain)
						tempTable.AddEndpoint(lrp1)
						lrp2 := createActualLRP(key, endpoint2, domain)
						tempTable.AddEndpoint(lrp2)
						// doing another swap to make sure the old table is still good
						table.Swap(tempTable, domainSet)
						_, messagesToEmit = table.Swap(tempTable, domainSet)
					})

					It("logs the collision", func() {
						lrp := createActualLRP(key, collisionEndpoint, domain)
						table.AddEndpoint(lrp)
						Eventually(logger).Should(Say(
							fmt.Sprintf(
								`\{"Address":\{"Host":"%s","Port":%d\},"instance_guid_a":"%s","instance_guid_b":"%s"`,
								endpoint1.Host,
								endpoint1.Port,
								endpoint1.InstanceGUID,
								collisionEndpoint.InstanceGUID,
							),
						))
					})

					It("should not emit anything since unregistrations were previously sent", func() {
						Expect(messagesToEmit).To(BeZero())
					})
				})
			})

			Describe("edge cases", func() {
				Context("when the original registration had no routes, and then the routing key loses endpoints", func() {
					BeforeEach(func() {
						//override previous set up
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						lrp1 := createActualLRP(key, endpoint1, domain)
						tempTable.AddEndpoint(lrp1)
						lrp2 := createActualLRP(key, endpoint2, domain)
						tempTable.AddEndpoint(lrp2)
						_, messagesToEmit = table.Swap(tempTable, domains)
						Expect(messagesToEmit.InternalUnregistrationMessages).To(HaveLen(2))

						tempTable = routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						lrp1 = createActualLRP(key, endpoint1, domain)
						tempTable.AddEndpoint(lrp1)
						_, messagesToEmit = table.Swap(tempTable, domains)
					})

					It("emits nothing", func() {
						Expect(messagesToEmit).To(BeZero())
					})
				})

				Context("when the original registration had no endpoints, and then the routing key loses a route", func() {
					BeforeEach(func() {
						//override previous set up
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1, hostname2)
						tempTable.SetRoutes(nil, schedulingInfo)
						table.Swap(tempTable, domains)

						tempTable = routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						schedulingInfo = createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1)
						tempTable.SetRoutes(nil, schedulingInfo)
						_, messagesToEmit = table.Swap(tempTable, domains)
					})

					It("emits nothing", func() {
						Expect(messagesToEmit).To(BeZero())
					})
				})
			})
		})
	})

	Describe("Processing deltas", func() {
		Context("when the table is empty", func() {
			Context("When setting routes", func() {
				It("emits nothing", func() {
					schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1, hostname2)
					_, messagesToEmit = table.SetRoutes(nil, schedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when removing routes", func() {
				It("emits nothing", func() {
					schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1, hostname2)
					_, messagesToEmit = table.RemoveRoutes(schedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when adding/updating endpoints", func() {
				It("emits nothing", func() {
					lrp1 := createActualLRP(key, endpoint1, domain)
					_, messagesToEmit := table.AddEndpoint(lrp1)
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when removing endpoints", func() {
				It("emits nothing", func() {
					lrp1 := createActualLRP(key, endpoint1, domain)
					_, messagesToEmit := table.RemoveEndpoint(lrp1)
					Expect(messagesToEmit).To(BeZero())
				})
			})
		})

		Context("when there are both endpoints and routes in the table", func() {
			var beforeLrpInfo *models.DesiredLRPSchedulingInfo
			BeforeEach(func() {
				tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
				routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")

				beforeLrpInfo = createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
				tempTable.SetRoutes(nil, beforeLrpInfo)
				lrp1 := createActualLRP(key, endpoint1, domain)
				tempTable.AddEndpoint(lrp1)
				lrp2 := createActualLRP(key, endpoint2, domain)
				tempTable.AddEndpoint(lrp2)

				table.Swap(tempTable, domains)
			})

			Describe("SetRoutes", func() {
				It("emits nothing when the route's hostnames do not change", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})

				It("emits unregistration and registration when the route service url changes", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "https://rs.example.com", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *newerTag)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)

					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: ""}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: ""}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: ""}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: ""}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				It("emits nothing when a hostname is added to a route with an older tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "https://rs.example.com", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *olderTag)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})

				It("emits registrations when a hostname is added to a route with a newer tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2, hostname3}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *newerTag)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)

					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				It("emits nothing when a hostname is removed from a route with an older tag", func() {
					schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *olderTag, hostname1)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})

				It("emits unregistrations when a hostname is removed from a route with a newer tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *newerTag)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)

					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				It("emits nothing when hostnames are added and removed from a route with an older tag", func() {
					schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *olderTag, hostname1, hostname3)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})

				It("emits registrations and unregistrations when hostnames are added and removed from a route with a newer tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname3}, []string{internalHostname2}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *newerTag)
					_, messagesToEmit = table.SetRoutes(beforeLrpInfo, schedulingInfo)

					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname3, LogGUID: logGuid}),
						},
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalRegistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 1, internalHostname2)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname2, fmt.Sprintf("%d.%s", 0, internalHostname2)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("RemoveRoutes", func() {
				It("emits unregistrations with a newer tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *newerTag)
					_, messagesToEmit = table.RemoveRoutes(schedulingInfo)

					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				It("updates routing table with a newer tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *newerTag)
					_, messagesToEmit = table.RemoveRoutes(schedulingInfo)
					Expect(table.HTTPAssociationsCount()).To(Equal(0))
					Expect(table.InternalAssociationsCount()).To(Equal(0))
				})

				It("emits unregistrations with the same tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					_, messagesToEmit = table.RemoveRoutes(schedulingInfo)

					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
							{
								Host:                 endpoint1.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
								PrivateInstanceIndex: "0",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				It("updates routing table with a same tag", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
					_, messagesToEmit = table.RemoveRoutes(schedulingInfo)
					Expect(table.HTTPAssociationsCount()).To(Equal(0))
					Expect(table.InternalAssociationsCount()).To(Equal(0))
				})

				It("emits nothing when the tag is older", func() {
					routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{}, "", []uint32{}, "")
					schedulingInfo := createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *olderTag)
					_, messagesToEmit = table.RemoveRoutes(schedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})

				It("does NOT update routing table with an older tag", func() {
					beforeRouteCount := table.HTTPAssociationsCount()
					beforeInternalRouteCount := table.InternalAssociationsCount()
					schedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *olderTag, hostname1, hostname2)
					_, messagesToEmit = table.RemoveRoutes(schedulingInfo)
					Expect(table.HTTPAssociationsCount()).To(Equal(beforeRouteCount))
					Expect(table.InternalAssociationsCount()).To(Equal(beforeInternalRouteCount))
				})
			})

			Context("AddEndpoint", func() {
				It("emits nothing when the tag is the same", func() {
					lrp1 := createActualLRP(key, endpoint1, domain)
					_, messagesToEmit := table.AddEndpoint(lrp1)
					Expect(messagesToEmit).To(BeZero())
				})

				It("emits nothing when updating an endpoint with an older tag", func() {
					updatedEndpoint := endpoint1
					updatedEndpoint.ModificationTag = olderTag
					lrp1 := createActualLRP(key, updatedEndpoint, domain)
					_, messagesToEmit := table.AddEndpoint(lrp1)

					Expect(messagesToEmit).To(BeZero())
				})

				It("emits nothing when updating an endpoint with a newer tag", func() {
					updatedEndpoint := endpoint1
					updatedEndpoint.ModificationTag = newerTag
					lrp1 := createActualLRP(key, updatedEndpoint, domain)
					_, messagesToEmit := table.AddEndpoint(lrp1)
					Expect(messagesToEmit).To(BeZero())
				})

				It("emits registrations when adding an endpoint", func() {
					lrp1 := createActualLRP(key, endpoint3, domain)
					_, messagesToEmit = table.AddEndpoint(lrp1)

					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint3, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalRegistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint3.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 2, internalHostname1)},
								PrivateInstanceIndex: "2",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				It("does not log a collision", func() {
					lrp := createActualLRP(key, endpoint3, domain)
					table.AddEndpoint(lrp)
					Consistently(logger).ShouldNot(Say("collision-detected-with-endpoint"))
				})

				Context("when adding an endpoint with IP and port that collide with existing endpoint", func() {
					var counterChan chan string
					BeforeEach(func() {
						counterChan = make(chan string, 10)
						fakeMetronClient.IncrementCounterStub = func(name string) error {
							counterChan <- name
							return nil
						}

						lrp := createActualLRP(key, collisionEndpoint, domain)
						table.AddEndpoint(lrp)
					})

					It("logs the collision", func() {
						Eventually(logger).Should(Say(
							fmt.Sprintf(
								`\{"Address":\{"Host":"%s","Port":%d\},"instance_guid_a":"%s","instance_guid_b":"%s"`,
								endpoint1.Host,
								endpoint1.Port,
								endpoint1.InstanceGUID,
								collisionEndpoint.InstanceGUID,
							),
						))
					})

					It("emits metrics about the address collisions", func() {
						Eventually(counterChan).Should(Receive(Equal("AddressCollisions")))
						Consistently(counterChan).ShouldNot(Receive())
					})
				})

				Context("when an evacuating endpoint is added for an instance that already exists", func() {
					It("emits nothing", func() {
						lrp1 := createActualLRP(key, evacuating1, domain)
						_, messagesToEmit = table.AddEndpoint(lrp1)
						Expect(messagesToEmit).To(BeZero())
					})
				})

				Context("when an instance endpoint is updated for an evacuating that already exists", func() {
					BeforeEach(func() {
						lrp1 := createActualLRP(key, evacuating1, domain)
						_, messagesToEmit = table.AddEndpoint(lrp1)
						table.AddEndpoint(lrp1)
					})

					It("emits nothing", func() {
						lrp2 := createActualLRP(key, endpoint1, domain)
						_, messagesToEmit = table.AddEndpoint(lrp2)
						Expect(messagesToEmit).To(BeZero())
					})
				})

				Context("when there are internal routes", func() {
					var internalHostname string
					BeforeEach(func() {
						tempTable := routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						internalHostname = "internal"
						routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{internalHostname}, "", []uint32{}, "")

						beforeLrpInfo = createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
						tempTable.SetRoutes(nil, beforeLrpInfo)
						lrp1 := createActualLRP(key, endpoint1, domain)
						tempTable.AddEndpoint(lrp1)
						lrp2 := createActualLRP(key, endpoint2, domain)
						tempTable.AddEndpoint(lrp2)

						table.Swap(tempTable, domains)
					})

					It("emits registrations when adding an endpoint", func() {
						lrp3 := createActualLRP(key, endpoint3, domain)
						_, messagesToEmit = table.AddEndpoint(lrp3)

						expected := []routingtable.RegistryMessage{
							{
								Host:                 endpoint3.ContainerIP,
								URIs:                 []string{internalHostname, fmt.Sprintf("%d.%s", 2, internalHostname)},
								PrivateInstanceIndex: "2",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						}

						Expect(messagesToEmit.InternalRegistrationMessages).To(Equal(expected))
					})

					Context("when the instance container port changes", func() {
						var changedEndpoint routingtable.Endpoint
						BeforeEach(func() {
							changedEndpoint = endpoint2
							changedEndpoint.ContainerPort = 1234
							changedEndpoint.ModificationTag = newerTag
						})

						It("emits nothing", func() {
							lrp := createActualLRP(key, changedEndpoint, domain)
							_, messagesToEmit := table.AddEndpoint(lrp)

							Expect(messagesToEmit.InternalRegistrationMessages).To(BeEmpty())
							Expect(messagesToEmit.InternalUnregistrationMessages).To(BeEmpty())
						})
					})

					Context("when the instance host port changes", func() {
						var changedEndpoint routingtable.Endpoint
						BeforeEach(func() {
							changedEndpoint = endpoint2
							changedEndpoint.Port = 1234
							changedEndpoint.ModificationTag = newerTag
						})

						It("emits nothing", func() {
							lrp := createActualLRP(key, changedEndpoint, domain)
							_, messagesToEmit := table.AddEndpoint(lrp)

							Expect(messagesToEmit.InternalRegistrationMessages).To(BeEmpty())
							Expect(messagesToEmit.InternalUnregistrationMessages).To(BeEmpty())
						})
					})

					Context("when an evacuating endpoint is added for an instance that already exists", func() {
						It("emits nothing", func() {
							lrp1 := createActualLRP(key, evacuating1, domain)
							_, messagesToEmit = table.AddEndpoint(lrp1)
							Expect(messagesToEmit).To(BeZero())
						})
					})

					Context("when an instance endpoint is updated for an evacuating that already exists", func() {
						BeforeEach(func() {
							lrp1 := createActualLRP(key, evacuating1, domain)
							_, messagesToEmit = table.AddEndpoint(lrp1)
							table.AddEndpoint(lrp1)
						})

						It("emits nothing", func() {
							lrp2 := createActualLRP(key, endpoint1, domain)
							_, messagesToEmit = table.AddEndpoint(lrp2)
							Expect(messagesToEmit).To(BeZero())
						})
					})
				})
			})

			Context("RemoveEndpoint", func() {
				It("emits unregistrations with the same tag", func() {
					lrp1 := createActualLRP(key, endpoint2, domain)
					_, messagesToEmit = table.RemoveEndpoint(lrp1)

					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				It("emits unregistrations when the tag is newer", func() {
					newerEndpoint := endpoint2
					newerEndpoint.ModificationTag = newerTag
					lrp1 := createActualLRP(key, newerEndpoint, domain)
					_, messagesToEmit = table.RemoveEndpoint(lrp1)

					expected := routingtable.MessagesToEmit{
						UnregistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						},
						InternalUnregistrationMessages: []routingtable.RegistryMessage{
							{
								Host:                 endpoint2.ContainerIP,
								URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
								PrivateInstanceIndex: "1",
								App:                  logGuid,
								Tags: map[string]string{
									"component": "route-emitter",
								},
							},
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})

				Context("when the instance has multiple ports, one of which has no routes", func() {
					var (
						lrp *routingtable.ActualLRPRoutingInfo
					)

					BeforeEach(func() {
						table = routingtable.NewRoutingTable(logger, false, fakeMetronClient)
						routes := createRoutingInfo(key.ContainerPort, []string{hostname1}, []string{internalHostname1}, "", []uint32{}, "")

						beforeLrpInfo = createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
						table.SetRoutes(nil, beforeLrpInfo)
						lrp = createActualLRPWithPortMappings(key, endpoint1, domain,
							models.NewPortMapping(endpoint1.Port+1, 2222),
							models.NewPortMapping(endpoint1.Port, 8080),
						)
						table.AddEndpoint(lrp)
					})

					It("emits unregistration message", func() {
						_, messages := table.RemoveEndpoint(lrp)
						expected := routingtable.MessagesToEmit{
							UnregistrationMessages: []routingtable.RegistryMessage{
								routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
							},
							InternalUnregistrationMessages: []routingtable.RegistryMessage{
								{
									Host:                 endpoint1.ContainerIP,
									URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
									PrivateInstanceIndex: "0",
									App:                  logGuid,
									Tags: map[string]string{
										"component": "route-emitter",
									},
								},
							},
						}
						Expect(messages).To(MatchMessagesToEmit(expected))
					})
				})

				It("emits nothing when the tag is older", func() {
					olderEndpoint := endpoint2
					olderEndpoint.ModificationTag = olderTag
					lrp1 := createActualLRP(key, olderEndpoint, domain)
					_, messagesToEmit = table.RemoveEndpoint(lrp1)
					Expect(messagesToEmit).To(BeZero())
				})

				Context("when an instance endpoint is removed for an instance that already exists", func() {
					BeforeEach(func() {
						lrp1 := createActualLRP(key, evacuating1, domain)
						_, messagesToEmit := table.AddEndpoint(lrp1)
						Expect(messagesToEmit).To(BeZero())
					})

					It("emits nothing", func() {
						lrp2 := createActualLRP(key, endpoint1, domain)
						_, messagesToEmit = table.RemoveEndpoint(lrp2)
						Expect(messagesToEmit).To(BeZero())
					})
				})

				Context("when a collision is avoided because the endpoint has already been removed", func() {
					It("does not log the collision", func() {
						lrp := createActualLRP(key, endpoint1, domain)
						table.RemoveEndpoint(lrp)
						lrp = createActualLRP(key, collisionEndpoint, domain)
						table.AddEndpoint(lrp)
						Consistently(logger).ShouldNot(Say("collision-detected-with-endpoint"))
					})
				})

				Context("when removing an endpoint that has a collision", func() {
					It("does logs the collision", func() {
						lrp := createActualLRP(key, collisionEndpoint, domain)
						table.RemoveEndpoint(lrp)
						Eventually(logger).Should(Say("collision-detected-with-endpoint"))
					})
				})
			})
		})

		Context("when there are only routes in the table", func() {
			var beforeLRPSchedulingInfo *models.DesiredLRPSchedulingInfo

			BeforeEach(func() {
				beforeLRPSchedulingInfo = createSchedulingInfo("https://rs.example.com")
				table.SetRoutes(nil, beforeLRPSchedulingInfo)
			})

			Context("When setting routes", func() {
				It("emits nothing", func() {
					after := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1, hostname3)
					table.SetRoutes(nil, beforeLRPSchedulingInfo)
					_, messagesToEmit = table.SetRoutes(beforeLRPSchedulingInfo, after)
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when removing routes", func() {
				It("emits nothing", func() {
					_, messagesToEmit = table.RemoveRoutes(beforeLRPSchedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when adding/updating endpoints", func() {
				It("emits registrations", func() {
					lrp1 := createActualLRP(key, endpoint1, domain)
					_, messagesToEmit = table.AddEndpoint(lrp1)

					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})
		})

		Context("when there are only endpoints in the table", func() {
			var beforeLRPSchedulingInfo *models.DesiredLRPSchedulingInfo
			var lrp1, lrp2 *routingtable.ActualLRPRoutingInfo
			BeforeEach(func() {
				lrp1 = createActualLRP(key, endpoint1, domain)
				lrp2 = createActualLRP(key, endpoint2, domain)
				table.AddEndpoint(lrp1)
				table.AddEndpoint(lrp2)
				beforeLRPSchedulingInfo = createSchedulingInfo("https://rs.example.com")
			})

			Context("When setting routes", func() {
				It("emits registrations", func() {
					_, messagesToEmit = table.SetRoutes(nil, beforeLRPSchedulingInfo)

					expected := routingtable.MessagesToEmit{
						RegistrationMessages: []routingtable.RegistryMessage{
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
							routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid, RouteServiceUrl: "https://rs.example.com"}),
						},
					}
					Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
				})
			})

			Context("when removing routes", func() {
				It("emits nothing", func() {
					_, messagesToEmit = table.RemoveRoutes(beforeLRPSchedulingInfo)
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when adding/updating endpoints", func() {
				It("emits nothing", func() {
					_, messagesToEmit = table.AddEndpoint(lrp2)
					Expect(messagesToEmit).To(BeZero())
				})
			})

			Context("when removing endpoints", func() {
				It("emits nothing", func() {
					_, messagesToEmit = table.RemoveEndpoint(lrp1)
					Expect(messagesToEmit).To(BeZero())
				})
			})
		})
	})

	Describe("GetRoutingEvents", func() {
		Context("when the table is empty", func() {
			It("should be empty", func() {
				_, messagesToEmit = table.GetRoutingEvents()
				Expect(messagesToEmit).To(BeZero())
			})
		})

		Context("when the table has routes but no endpoints", func() {
			var beforeLRPSchedulingInfo *models.DesiredLRPSchedulingInfo
			BeforeEach(func() {
				routes := createRoutingInfo(key.ContainerPort, []string{}, []string{}, "https://rs.example.com", []uint32{}, "")
				beforeLRPSchedulingInfo = createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
				table.SetRoutes(nil, beforeLRPSchedulingInfo)
			})

			It("should be empty", func() {
				_, messagesToEmit = table.GetRoutingEvents()
				Expect(messagesToEmit).To(BeZero())
			})
		})

		Context("when the table has endpoints but no routes", func() {
			var lrp1, lrp2 *routingtable.ActualLRPRoutingInfo

			BeforeEach(func() {
				lrp1 = createActualLRP(key, endpoint1, domain)
				lrp2 = createActualLRP(key, endpoint2, domain)
				table.AddEndpoint(lrp1)
				table.AddEndpoint(lrp2)
			})

			It("should be empty", func() {
				_, messagesToEmit = table.GetRoutingEvents()
				Expect(messagesToEmit).To(BeZero())
			})
		})

		Context("when the table has routes and endpoints", func() {
			var beforeLRPSchedulingInfo *models.DesiredLRPSchedulingInfo
			var lrp1, lrp2 *routingtable.ActualLRPRoutingInfo

			BeforeEach(func() {
				routes := createRoutingInfo(key.ContainerPort, []string{hostname1, hostname2}, []string{internalHostname1}, "", []uint32{}, "")
				beforeLRPSchedulingInfo = createSchedulingInfoWithRoutes(key.ProcessGUID, 3, routes, logGuid, *currentTag)
				table.SetRoutes(nil, beforeLRPSchedulingInfo)
				lrp1 = createActualLRP(key, endpoint1, domain)
				lrp2 = createActualLRP(key, endpoint2, domain)
				table.AddEndpoint(lrp1)
				table.AddEndpoint(lrp2)
			})

			It("emits the registrations", func() {
				_, messagesToEmit = table.GetRoutingEvents()

				expected := routingtable.MessagesToEmit{
					RegistrationMessages: []routingtable.RegistryMessage{
						routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
						routingtable.RegistryMessageFor(endpoint1, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
						routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname1, LogGUID: logGuid}),
						routingtable.RegistryMessageFor(endpoint2, routingtable.Route{Hostname: hostname2, LogGUID: logGuid}),
					},
					InternalRegistrationMessages: []routingtable.RegistryMessage{
						{
							Host:                 endpoint2.ContainerIP,
							URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 1, internalHostname1)},
							PrivateInstanceIndex: "1",
							App:                  logGuid,
							Tags: map[string]string{
								"component": "route-emitter",
							},
						},
						{
							Host:                 endpoint1.ContainerIP,
							URIs:                 []string{internalHostname1, fmt.Sprintf("%d.%s", 0, internalHostname1)},
							PrivateInstanceIndex: "0",
							App:                  logGuid,
							Tags: map[string]string{
								"component": "route-emitter",
							},
						},
					},
				}
				Expect(messagesToEmit).To(MatchMessagesToEmit(expected))
			})
		})
	})

	Describe("HasExternalRoutes", func() {
		It("returns true if the actual lrp has external routes ", func() {
			beforeLRPSchedulingInfo := createDesiredLRPSchedulingInfo(key.ProcessGUID, int32(3), key.ContainerPort, logGuid, "", *currentTag, hostname1, hostname2)
			table.SetRoutes(nil, beforeLRPSchedulingInfo)
			lrp1 := createActualLRP(key, endpoint1, domain)
			table.AddEndpoint(lrp1)
			Expect(table.HasExternalRoutes(lrp1)).To(BeTrue())
		})
	})

	Describe("RouteCount", func() {
		It("returns 0 on a new routing table", func() {
			Expect(table.HTTPAssociationsCount()).To(Equal(0))
		})

		It("returns 1 after adding a route to a single process", func() {
			schedulingInfo := createDesiredLRPSchedulingInfo("fake-process-guid", int32(3), 0, logGuid, "", *currentTag, "fake-route-url")
			table.SetRoutes(nil, schedulingInfo)
			lrp := createActualLRP(routingtable.RoutingKey{ProcessGUID: "fake-process-guid"}, routingtable.Endpoint{InstanceGUID: "fake-instance-guid", ModificationTag: currentTag}, domain)
			table.AddEndpoint(lrp)

			Expect(table.HTTPAssociationsCount()).To(Equal(1))
		})

		It("returns 2 after associating 2 urls with a single process", func() {
			schedulingInfo := createDesiredLRPSchedulingInfo("fake-process-guid", int32(3), 0, logGuid, "", *currentTag, "fake-route-url-1", "fake-route-url-2")
			table.SetRoutes(nil, schedulingInfo)
			lrp := createActualLRP(routingtable.RoutingKey{ProcessGUID: "fake-process-guid"}, routingtable.Endpoint{InstanceGUID: "fake-instance-guid-1", ModificationTag: currentTag}, domain)
			table.AddEndpoint(lrp)

			Expect(table.HTTPAssociationsCount()).To(Equal(2))
		})

		It("returns 8 after associating 2 urls with 2 processes with 2 instances each", func() {
			schedulingInfo := createDesiredLRPSchedulingInfo("fake-process-guid-a", int32(3), 0, logGuid, "", *currentTag, "fake-route-url-a1", "fake-route-url-a2")
			table.SetRoutes(nil, schedulingInfo)
			lrp := createActualLRP(routingtable.RoutingKey{ProcessGUID: "fake-process-guid-a"}, routingtable.Endpoint{InstanceGUID: "fake-instance-guid-a1", ModificationTag: currentTag}, domain)
			table.AddEndpoint(lrp)
			lrp = createActualLRP(routingtable.RoutingKey{ProcessGUID: "fake-process-guid-a"}, routingtable.Endpoint{InstanceGUID: "fake-instance-guid-a2", ModificationTag: currentTag}, domain)
			table.AddEndpoint(lrp)

			schedulingInfo = createDesiredLRPSchedulingInfo("fake-process-guid-b", int32(3), 0, logGuid, "", *currentTag, "fake-route-url-b1", "fake-route-url-b2")
			table.SetRoutes(nil, schedulingInfo)
			lrp = createActualLRP(routingtable.RoutingKey{ProcessGUID: "fake-process-guid-b"}, routingtable.Endpoint{InstanceGUID: "fake-instance-guid-b1", ModificationTag: currentTag}, domain)
			table.AddEndpoint(lrp)
			lrp = createActualLRP(routingtable.RoutingKey{ProcessGUID: "fake-process-guid-b"}, routingtable.Endpoint{InstanceGUID: "fake-instance-guid-b2", ModificationTag: currentTag}, domain)
			table.AddEndpoint(lrp)

			Expect(table.HTTPAssociationsCount()).To(Equal(8))
		})
	})
})
