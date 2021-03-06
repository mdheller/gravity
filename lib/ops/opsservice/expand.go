/*
Copyright 2018 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package opsservice

import (
	"context"

	"github.com/gravitational/gravity/lib/ops"
	"github.com/gravitational/gravity/lib/schema"
	"github.com/gravitational/gravity/lib/storage"

	teleservices "github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

// createExpandOperation initiates expand operation
func (s *site) createExpandOperation(req ops.CreateSiteExpandOperationRequest) (*ops.SiteOperationKey, error) {
	log.Debugf("createExpandOperation(%#v)", req)

	profiles := make(map[string]storage.ServerProfile)
	for role, count := range req.Servers {
		profile, err := s.app.Manifest.NodeProfiles.ByName(role)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		profiles[role] = storage.ServerProfile{
			Description: profile.Description,
			Labels:      profile.Labels,
			ServiceRole: string(profile.ServiceRole),
			Request: storage.ServerProfileRequest{
				Count: count,
			},
		}
	}
	return s.createInstallExpandOperation(
		ops.OperationExpand, ops.OperationStateExpandInitiated, req.Provisioner,
		req.Variables, profiles)
}

func (s *site) getSiteOperation(operationID string) (*ops.SiteOperation, error) {
	op, err := s.backend().GetSiteOperation(s.key.SiteDomain, operationID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return (*ops.SiteOperation)(op), nil
}

// expandOperationStart kicks off actuall expansion process:
// resource provisioning, package configuration and deployment
func (s *site) expandOperationStart(ctx *operationContext) error {
	op, err := s.compareAndSwapOperationState(swap{
		key: ctx.key(),
		expectedStates: []string{
			ops.OperationStateExpandInitiated,
			ops.OperationStateExpandPrechecks,
		},
		newOpState: ops.OperationStateExpandProvisioning,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	if isAWSProvisioner(op.Provisioner) {
		if !s.app.Manifest.HasHook(schema.HookNodesProvision) {
			return trace.NotFound("%v hook is not defined",
				schema.HookNodesProvision)
		}
		ctx.Infof("Using nodes provisioning hook.")
		err := s.runNodesProvisionHook(ctx)
		if err != nil {
			return trace.Wrap(err)
		}
		ctx.RecordInfo("Infrastructure has been successfully provisioned.")
	}

	s.reportProgress(ctx, ops.ProgressEntry{
		State:   ops.ProgressStateInProgress,
		Message: "Waiting for the provisioned node to come up",
	})

	_, err = s.waitForAgents(context.TODO(), ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	s.reportProgress(ctx, ops.ProgressEntry{
		State:   ops.ProgressStateInProgress,
		Message: "The node is up",
	})

	op, err = s.compareAndSwapOperationState(swap{
		key:            ctx.key(),
		expectedStates: []string{ops.OperationStateExpandProvisioning},
		newOpState:     ops.OperationStateReady,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	err = s.waitForOperation(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (s *site) validateExpand(op *ops.SiteOperation, req *ops.OperationUpdateRequest) error {
	if op.Provisioner == schema.ProvisionerOnPrem {
		if len(req.Servers) > 1 {
			return trace.BadParameter(
				"can only add one node at a time, stop agents on %v extra node(-s)", len(req.Servers)-1)
		} else if len(req.Servers) == 0 {
			return trace.BadParameter(
				"no servers provided, run agent command on the node you want to join")
		}
	}
	for role, _ := range req.Profiles {
		profile, err := s.app.Manifest.NodeProfiles.ByName(role)
		if err != nil {
			return trace.Wrap(err)
		}
		if profile.ExpandPolicy == schema.ExpandPolicyFixed {
			return trace.BadParameter(
				"server profile %q does not allow expansion", role)
		}
	}

	labels := map[string]string{
		schema.ServiceLabelRole: string(schema.ServiceRoleMaster),
	}
	masters, err := s.teleport().GetServers(context.TODO(), s.domainName, labels)
	if err != nil {
		return trace.Wrap(err)
	}

	err = setClusterRoles(req.Servers, *s.app, len(masters))
	return trace.Wrap(err)
}

func (s *site) getTeleportSecrets() (*teleportSecrets, error) {
	withPrivateKey := true
	authorities, err := s.teleport().CertAuthorities(withPrivateKey)
	if err != nil {
		return nil, trace.Wrap(err, "failed to query cert authorities")
	}

	var hostPrivateKey, userPrivateKey []byte
	for _, ca := range authorities {
		if len(ca.GetSigningKeys()) == 0 {
			log.Errorf("no signing key of type %v", ca.GetType())
			continue
		}
		switch ca.GetType() {
		case teleservices.HostCA:
			hostPrivateKey = ca.GetSigningKeys()[0]
		case teleservices.UserCA:
			userPrivateKey = ca.GetSigningKeys()[0]
		}
	}

	var errors []error
	if hostPrivateKey == nil {
		errors = append(errors, trace.NotFound("host CA not found"))
	}
	if userPrivateKey == nil {
		errors = append(errors, trace.NotFound("user CA not found"))
	}
	if len(errors) > 0 {
		return nil, trace.NewAggregate(errors...)
	}

	hostKey, err := ssh.ParsePrivateKey(hostPrivateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	hostPublicKey := ssh.MarshalAuthorizedKey(hostKey.PublicKey())

	userKey, err := ssh.ParsePrivateKey(userPrivateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	userPublicKey := ssh.MarshalAuthorizedKey(userKey.PublicKey())

	return &teleportSecrets{
		HostCAPrivateKey: hostPrivateKey,
		HostCAPublicKey:  hostPublicKey,
		UserCAPrivateKey: userPrivateKey,
		UserCAPublicKey:  userPublicKey,
	}, nil
}
