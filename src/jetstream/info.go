package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"fmt"

	"github.com/cloudfoundry-incubator/stratos/src/jetstream/repository/interfaces"
	"github.com/labstack/echo"
	log "github.com/sirupsen/logrus"
)

// Endpoint - This represents the CNSI endpoint
type Endpoint struct {
	GUID     string                    `json:"guid"`
	Name     string                    `json:"name"`
	Version  string                    `json:"version"`
	User     *interfaces.ConnectedUser `json:"user"`
	CNSIType string                    `json:"type"`
}

func (p *portalProxy) info(c echo.Context) error {

	s, err := p.getInfo(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	}

	return c.JSON(http.StatusOK, s)
}

// Add a set of endpoint relations to each endpoint via the relations table
func (p *portalProxy) updateEndpointsWithRelations(endpoints map[string]map[string]*interfaces.EndpointDetail) error {
	relations, err := p.ListRelations()
	if err != nil {
		return fmt.Errorf("Failed to fetch relations: %v", err)
	}
	for _, endpointsOfType := range endpoints {
		for _, endpoint := range endpointsOfType {
			if (endpoint.Relations == nil) {
				endpoint.Relations = &interfaces.EndpointRelations{
					Provides: []interfaces.EndpointRelation{},
					Receives: []interfaces.EndpointRelation{},
				}
			}
			for _, relation := range relations {
				// Add relation to appropriate Provider/Target collection
				if relation.Provider == endpoint.GUID {
					endpoint.Relations.Provides = append(endpoint.Relations.Provides, interfaces.EndpointRelation{
						Guid:         relation.Target,
						RelationType: relation.RelationType,
						Metadata:     relation.Metadata,
					})
				} else if relation.Target == endpoint.GUID {
					endpoint.Relations.Receives = append(endpoint.Relations.Receives, interfaces.EndpointRelation{
						Guid:         relation.Provider,
						RelationType: relation.RelationType,
						Metadata:     relation.Metadata,
					})
				}
			}
		}
	}

	return nil
}

func (p *portalProxy) getInfo(c echo.Context) (*interfaces.Info, error) {
	// get the version
	versions, err := p.getVersionsData()
	if err != nil {
		return nil, errors.New("Could not find database version")
	}

	// get the user
	userGUID, err := p.GetSessionStringValue(c, "user_id")
	if err != nil {
		return nil, errors.New("Could not find session user_id")
	}

	uaaUser, err := p.GetUAAUser(userGUID)
	if err != nil {
		return nil, errors.New("Could not load session user data")
	}

	// create initial info struct
	s := &interfaces.Info{
		Versions:     versions,
		User:         uaaUser,
		Endpoints:    make(map[string]map[string]*interfaces.EndpointDetail),
		CloudFoundry: p.Config.CloudFoundryInfo,
		PluginConfig: p.Config.PluginConfig,
	}

	// Only add diagnostics information if the user is an admin
	if uaaUser.Admin {
		s.Diagnostics = p.Diagnostics
	}

	// initialize the Endpoints maps
	for _, plugin := range p.Plugins {
		endpointPlugin, err := plugin.GetEndpointPlugin()
		if err != nil {
			// Plugin doesn't implement an Endpoint Plugin interface, skip
			continue
		}
		// Empty Type can be used if a plugin just wants to implement UpdateMetadata
		if len(endpointPlugin.GetType()) > 0 {
			s.Endpoints[endpointPlugin.GetType()] = make(map[string]*interfaces.EndpointDetail)
		}
	}

	// get the CNSI Endpoints
	cnsiList, _ := p.buildCNSIList(c)
	for _, cnsi := range cnsiList {
		// Extend the CNSI record
		endpoint := &interfaces.EndpointDetail{
			CNSIRecord:        cnsi,
			EndpointMetadata:  marshalEndpointMetadata(cnsi.Metadata),
			Metadata:          make(map[string]string),
			SystemSharedToken: false,
		}

		// try to get the user info for this cnsi for the user
		cnsiUser, token, ok := p.GetCNSIUserAndToken(cnsi.GUID, userGUID)
		if ok {
			endpoint.User = cnsiUser
			endpoint.TokenMetadata = token.Metadata
			endpoint.SystemSharedToken = token.SystemShared
		}
		cnsiType := cnsi.CNSIType
		s.Endpoints[cnsiType][cnsi.GUID] = endpoint
	}

	err = p.updateEndpointsWithRelations(s.Endpoints)
	if err != nil {
		log.Warnf("Failed to add relations data to endpoints during info request: %v", err)
	}

	// Allow plugin to modify the info data
	for _, plugin := range p.Plugins {
		endpointPlugin, err := plugin.GetEndpointPlugin()
		if err == nil {
			endpointPlugin.UpdateMetadata(s, userGUID, c)
		}
	}

	s.Plugins = p.PluginsStatus

	return s, nil
}

func marshalEndpointMetadata(metadata string) interface{} {
	if len(metadata) > 2 && strings.Index(metadata, "{") == 0 {
		var anyJSON map[string]interface{}
		json.Unmarshal([]byte(metadata), &anyJSON)
		return anyJSON
	} else {
		return metadata
	}
}
