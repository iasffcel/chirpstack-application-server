package auth

import (
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/net/context"

	"github.com/brocaar/chirpstack-application-server/internal/backend/networkserver"
	"github.com/brocaar/chirpstack-application-server/internal/backend/networkserver/mock"
	"github.com/brocaar/chirpstack-application-server/internal/storage"
	"github.com/brocaar/chirpstack-application-server/internal/test"
	"github.com/brocaar/lorawan"
)

type validatorTest struct {
	Name       string
	Claims     Claims
	Validators []ValidatorFunc
	ExpectedOK bool
}

type ValidatorTestSuite struct {
	suite.Suite

	networkServers []storage.NetworkServer
	organizations  []storage.Organization
}

func (ts *ValidatorTestSuite) SetupSuite() {
	assert := require.New(ts.T())

	conf := test.GetConfig()
	assert.NoError(storage.Setup(conf))

	nsClient := mock.NewClient()
	networkserver.SetPool(mock.NewPool(nsClient))
}

func (ts *ValidatorTestSuite) SetupTest() {
	assert := require.New(ts.T())

	test.MustResetDB(storage.DB().DB)

	ts.networkServers = []storage.NetworkServer{
		{Name: "test-ns", Server: "test-ns:1234"},
		{Name: "test-ns-2", Server: "test-ns-2:1234"},
	}
	for i := range ts.networkServers {
		assert.NoError(storage.CreateNetworkServer(context.Background(), storage.DB(), &ts.networkServers[i]))
	}

	ts.organizations = []storage.Organization{
		{Name: "organization-1", CanHaveGateways: true},
		{Name: "organization-2", CanHaveGateways: false},
	}
	for i := range ts.organizations {
		assert.NoError(storage.CreateOrganization(context.Background(), storage.DB(), &ts.organizations[i]))
	}
}

func (ts *ValidatorTestSuite) CreateUser(username string, isActive, isAdmin bool) (int64, error) {
	u := storage.User{
		Username: username,
		IsAdmin:  isAdmin,
		IsActive: isActive,
		Email:    username + "@example.com",
	}

	return storage.CreateUser(context.Background(), storage.DB(), &u, "v3rys3cr3t!")
}

func (ts *ValidatorTestSuite) RunTests(t *testing.T, tests []validatorTest) {
	for _, tst := range tests {
		t.Run(tst.Name, func(t *testing.T) {
			assert := require.New(t)

			if tst.Claims.Username != "" {
				tst.Claims.Subject = "user"
			} else {
				tst.Claims.Subject = "api_key"
			}

			for _, v := range tst.Validators {
				ok, err := v(storage.DB(), &tst.Claims)
				assert.NoError(err)
				assert.Equal(tst.ExpectedOK, ok)
			}
		})
	}
}

func (ts *ValidatorTestSuite) TestUser() {
	assert := require.New(ts.T())

	users := []struct {
		id       int64
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "activeUser2", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}
	for i, user := range users {
		id, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
		users[i].id = id
	}

	orgUsers := []struct {
		id             int64
		organizationID int64
		username       string
		isAdmin        bool
	}{
		{organizationID: ts.organizations[0].ID, username: "orgAdmin", isAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "orgUser", isAdmin: false},
	}
	for i, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)
		orgUsers[i].id = id

		assert.NoError(storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, false, false))
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	ts.T().Run("ActiveUser", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "active user",
				Validators: []ValidatorFunc{ValidateActiveUser()},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: true,
			},
			{
				Name:       "inactive user",
				Validators: []ValidatorFunc{ValidateActiveUser()},
				Claims:     Claims{Username: "inactiveAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "invalid user",
				Validators: []ValidatorFunc{ValidateActiveUser()},
				Claims:     Claims{Username: "wrongUser"},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("UsersAccess (DisableAssignExistingUsers = false)", func(t *testing.T) {
		DisableAssignExistingUsers = false
		tests := []validatorTest{
			{
				Name:       "global admin user can create and list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "inactive global admin user can not create or list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{Username: "inactiveAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "organization admin user can create and list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{Username: "orgAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can create and list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "normal user can not create or list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{Username: "orgUser"},
				ExpectedOK: false,
			},
			{
				Name:       "other api keys can not create or list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}
		ts.RunTests(t, tests)
	})

	ts.T().Run("UsersAccess (DisableAssignExistingUsers = true)", func(t *testing.T) {
		DisableAssignExistingUsers = true
		tests := []validatorTest{
			{
				Name:       "global admin user can create and list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create), ValidateUsersAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin user can create",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create)},
				Claims:     Claims{Username: "orgAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can create",
				Validators: []ValidatorFunc{ValidateUsersAccess(Create)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin user can not list",
				Validators: []ValidatorFunc{ValidateUsersAccess(List)},
				Claims:     Claims{Username: "orgAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "organization api key can not list",
				Validators: []ValidatorFunc{ValidateUsersAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
		}
		ts.RunTests(t, tests)
	})

	ts.T().Run("UserAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin user has access to read, update and delete",
				Validators: []ValidatorFunc{ValidateUserAccess(users[2].id, Read), ValidateUserAccess(users[2].id, Update), ValidateUserAccess(users[2].id, Delete)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "admin api key has access to read, update and delete",
				Validators: []ValidatorFunc{ValidateUserAccess(users[2].id, Read), ValidateUserAccess(users[2].id, Update), ValidateUserAccess(users[2].id, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "user itself has access to read",
				Validators: []ValidatorFunc{ValidateUserAccess(users[2].id, Read)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: true,
			},
			{
				Name:       "user itself has no access to update or delete",
				Validators: []ValidatorFunc{ValidateUserAccess(users[2].id, Update), ValidateUserAccess(users[2].id, Delete)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "other users are not able to read, update or delete",
				Validators: []ValidatorFunc{ValidateUserAccess(users[2].id, Read), ValidateUserAccess(users[2].id, Update), ValidateUserAccess(users[2].id, Delete)},
				Claims:     Claims{Username: "activeUser2"},
				ExpectedOK: false,
			},
			{
				Name:       "non admin api key can not read, update or delete",
				Validators: []ValidatorFunc{ValidateUserAccess(users[2].id, Read), ValidateUserAccess(users[2].id, Update), ValidateUserAccess(users[2].id, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestGateway() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}

	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for _, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org - can have gateways", OrganizationID: &ts.organizations[0].ID},
		{Name: "org - can not have gateways", OrganizationID: &ts.organizations[1].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	ts.T().Run("GatewayProfileAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create, update, delete read and list",
				Validators: []ValidatorFunc{ValidateGatewayProfileAccess(Create), ValidateGatewayProfileAccess(Update), ValidateGatewayProfileAccess(Delete), ValidateGatewayProfileAccess(Read), ValidateGatewayProfileAccess(List)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "normal users can read and list",
				Validators: []ValidatorFunc{ValidateGatewayProfileAccess(Read), ValidateGatewayProfileAccess(List)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: true,
			},
			{
				Name:       "normal users can not create, update and delete",
				Validators: []ValidatorFunc{ValidateGatewayProfileAccess(Create), ValidateGatewayProfileAccess(Update), ValidateGatewayProfileAccess(Delete)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("GatewaysAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID), ValidateGatewaysAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create and list (org CanHaveGateways=true)",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID), ValidateGatewaysAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "gateway admin users can create and list (org CanHaveGateways=true)",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID), ValidateGatewaysAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserGatewayAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "normal user can list",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(List, 0)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can not create (org CanHaveGateways=false)",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[1].ID)},
				Claims:     Claims{Username: "org1ActiveUserAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "gateway admin users can not create (org CanHaveGateways=true)",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[1].ID)},
				Claims:     Claims{Username: "org1ActiveUserGatewayAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "organization user can not create",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "normal user can not create",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "inactive user can not list",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(List, 0)},
				Claims:     Claims{Username: "inactiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID), ValidateGatewaysAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can create and list (org CanHaveGateways=true)",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID), ValidateGatewaysAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can not create (org CanHaveGateways=false)",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[1].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not create or list",
				Validators: []ValidatorFunc{ValidateGatewaysAccess(Create, ts.organizations[0].ID), ValidateGatewaysAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("TestGatewayAccess", func(t *testing.T) {
		assert := require.New(t)

		gateways := []storage.Gateway{
			{MAC: lorawan.EUI64{1, 1, 1, 1, 1, 1, 1, 1}, Name: "gateway1", OrganizationID: ts.organizations[0].ID, NetworkServerID: ts.networkServers[0].ID},
		}
		for i := range gateways {
			assert.NoError(storage.CreateGateway(context.Background(), storage.DB(), &gateways[i]))
		}

		tests := []validatorTest{
			{
				Name:       "global admin users can create, update and delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC), ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create, update and delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC), ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization gateway admin users can create, update and delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC), ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{Username: "org0ActiveUserGatewayAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not update or delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "normal users can not read, update or delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC), ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC), ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization key can read, update and delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC), ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "oter api key can not read, update or delete",
				Validators: []ValidatorFunc{ValidateGatewayAccess(Read, gateways[0].MAC), ValidateGatewayAccess(Update, gateways[0].MAC), ValidateGatewayAccess(Delete, gateways[0].MAC)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestApplication() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}

	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}
	for _, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	applications := []storage.Application{
		{OrganizationID: ts.organizations[0].ID, Name: "application-1", ServiceProfileID: serviceProfileIDs[0]},
	}
	for i := range applications {
		assert.NoError(storage.CreateApplication(context.Background(), storage.DB(), &applications[i]))
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "app", ApplicationID: &applications[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	ts.T().Run("ApplicationsAcccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID), ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create and list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID), ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization device admin users can create and list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID), ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserDeviceAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "normal users can list when no organization id is given",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(List, 0)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not create",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "normal users can not create and list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID), ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID), ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can create and list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID), ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "application api key can not create or list",
				Validators: []ValidatorFunc{ValidateApplicationsAccess(Create, ts.organizations[0].ID), ValidateApplicationsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("ApplicationAccess", func(t *testing.T) {

		tests := []validatorTest{
			{
				Name:       "global admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read), ValidateApplicationAccess(applications[0].ID, Update), ValidateApplicationAccess(applications[0].ID, Delete)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read update and delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read), ValidateApplicationAccess(applications[0].ID, Update), ValidateApplicationAccess(applications[0].ID, Delete)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization device admin users can read update and delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read), ValidateApplicationAccess(applications[0].ID, Update), ValidateApplicationAccess(applications[0].ID, Delete)},
				Claims:     Claims{Username: "org0ActiveUserDeviceAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "other users can not read, update or delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(1, Read), ValidateApplicationAccess(1, Update), ValidateApplicationAccess(1, Delete)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read), ValidateApplicationAccess(applications[0].ID, Update), ValidateApplicationAccess(applications[0].ID, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read), ValidateApplicationAccess(applications[0].ID, Update), ValidateApplicationAccess(applications[0].ID, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "application api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read), ValidateApplicationAccess(applications[0].ID, Update), ValidateApplicationAccess(applications[0].ID, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "invalid api key can read, update or delete",
				Validators: []ValidatorFunc{ValidateApplicationAccess(applications[0].ID, Read), ValidateApplicationAccess(applications[0].ID, Update), ValidateApplicationAccess(applications[0].ID, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestDevice() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}

	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for _, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	applications := []storage.Application{
		{OrganizationID: ts.organizations[0].ID, Name: "application-1", ServiceProfileID: serviceProfileIDs[0]},
	}
	for i := range applications {
		assert.NoError(storage.CreateApplication(context.Background(), storage.DB(), &applications[i]))
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "app", ApplicationID: &applications[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	deviceProfiles := []storage.DeviceProfile{
		{Name: "test-dp-1", OrganizationID: ts.organizations[0].ID, NetworkServerID: ts.networkServers[0].ID},
	}
	var deviceProfilesIDs []uuid.UUID
	for i := range deviceProfiles {
		assert.NoError(storage.CreateDeviceProfile(context.Background(), storage.DB(), &deviceProfiles[i]))
		dpID, _ := uuid.FromBytes(deviceProfiles[i].DeviceProfile.Id)
		deviceProfilesIDs = append(deviceProfilesIDs, dpID)
	}

	devices := []storage.Device{
		{DevEUI: lorawan.EUI64{1, 1, 1, 1, 1, 1, 1, 1}, Name: "test-1", ApplicationID: applications[0].ID, DeviceProfileID: deviceProfilesIDs[0]},
	}
	for i := range devices {
		assert.NoError(storage.CreateDevice(context.Background(), storage.DB(), &devices[i]))
	}

	ts.T().Run("DevicesAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin user has access to create and list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create and list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization device admin users can create and list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{Username: "org0ActiveUserDeviceAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not create",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "other users can not create or list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can create and list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "application api key can create and list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "empty api key can not create or list",
				Validators: []ValidatorFunc{ValidateNodesAccess(applications[0].ID, Create), ValidateNodesAccess(applications[0].ID, List)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("DeviceAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read), ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read), ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization device admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read), ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "org0ActiveUserDeviceAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users (non-admin) can not update or delete",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "other users can not read, update and delete",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read), ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "organization api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read), ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "application api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read), ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "empty api key can not read, update or list",
				Validators: []ValidatorFunc{ValidateNodeAccess(devices[0].DevEUI, Read), ValidateNodeAccess(devices[0].DevEUI, Update), ValidateNodeAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("DeviceQueueAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read, list, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceQueueAccess(devices[0].DevEUI, Create), ValidateDeviceQueueAccess(devices[0].DevEUI, List), ValidateDeviceQueueAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read, list, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceQueueAccess(devices[0].DevEUI, Create), ValidateDeviceQueueAccess(devices[0].DevEUI, List), ValidateDeviceQueueAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "other users can not read, list, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceQueueAccess(devices[0].DevEUI, Create), ValidateDeviceQueueAccess(devices[0].DevEUI, List), ValidateDeviceQueueAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, list, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceQueueAccess(devices[0].DevEUI, Create), ValidateDeviceQueueAccess(devices[0].DevEUI, List), ValidateDeviceQueueAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key can read, list, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceQueueAccess(devices[0].DevEUI, Create), ValidateDeviceQueueAccess(devices[0].DevEUI, List), ValidateDeviceQueueAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "application api key can read, list, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceQueueAccess(devices[0].DevEUI, Create), ValidateDeviceQueueAccess(devices[0].DevEUI, List), ValidateDeviceQueueAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "empty api key can read, list, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceQueueAccess(devices[0].DevEUI, Create), ValidateDeviceQueueAccess(devices[0].DevEUI, List), ValidateDeviceQueueAccess(devices[0].DevEUI, Delete)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestDeviceProfile() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}

	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for _, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	applications := []storage.Application{
		{OrganizationID: ts.organizations[0].ID, Name: "application-1", ServiceProfileID: serviceProfileIDs[0]},
		{OrganizationID: ts.organizations[1].ID, Name: "application-2", ServiceProfileID: serviceProfileIDs[0]},
	}
	for i := range applications {
		assert.NoError(storage.CreateApplication(context.Background(), storage.DB(), &applications[i]))
	}

	deviceProfiles := []storage.DeviceProfile{
		{Name: "test-dp-1", OrganizationID: ts.organizations[0].ID, NetworkServerID: ts.networkServers[0].ID},
	}
	var deviceProfilesIDs []uuid.UUID
	for i := range deviceProfiles {
		assert.NoError(storage.CreateDeviceProfile(context.Background(), storage.DB(), &deviceProfiles[i]))
		dpID, _ := uuid.FromBytes(deviceProfiles[i].DeviceProfile.Id)
		deviceProfilesIDs = append(deviceProfilesIDs, dpID)
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "app", ApplicationID: &applications[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	ts.T().Run("DeviceProfilesAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0), ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create and list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0), ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization device admin users can create and list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0), ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{Username: "org0ActiveUserDeviceAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list with an application id is given",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(List, 0, applications[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "any user can list when organization id = 0",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(List, 0, 0)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not create",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not create or list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0), ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{Username: "org1ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not list when an application id is given beloning to a different organization",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(List, 0, applications[1].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0), ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can create and list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0), ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(List, 0, applications[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can not create",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, 0, applications[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not create or list",
				Validators: []ValidatorFunc{ValidateDeviceProfilesAccess(Create, ts.organizations[0].ID, 0), ValidateDeviceProfilesAccess(List, ts.organizations[0].ID, 0)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("DeviceProfileAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization device admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{Username: "org0ActiveUserDeviceAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0])},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not update and delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not read, update ande delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{Username: "org1ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can read",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can not update or delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not read, update or delete",
				Validators: []ValidatorFunc{ValidateDeviceProfileAccess(Read, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Update, deviceProfilesIDs[0]), ValidateDeviceProfileAccess(Delete, deviceProfilesIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestNetworkServer() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}

	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for _, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	ts.T().Run("NetworkServersAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(Create, ts.organizations[0].ID), ValidateNetworkServersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not create",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not create or list",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(Create, ts.organizations[0].ID), ValidateNetworkServersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(Create, ts.organizations[0].ID), ValidateNetworkServersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can list",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can not create",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not create or list",
				Validators: []ValidatorFunc{ValidateNetworkServersAccess(Create, ts.organizations[0].ID), ValidateNetworkServersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}
		ts.RunTests(t, tests)
	})

	ts.T().Run("NetworkServerAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Read, ts.networkServers[0].ID), ValidateNetworkServerAccess(Update, ts.networkServers[0].ID), ValidateNetworkServerAccess(Delete, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Read, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization gateway admin users can read",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Read, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserGatewayAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can not update and delete",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Update, ts.networkServers[0].ID), ValidateNetworkServerAccess(Delete, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "regular users can not read, update and delete",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Read, ts.networkServers[0].ID), ValidateNetworkServerAccess(Update, ts.networkServers[0].ID), ValidateNetworkServerAccess(Delete, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Read, ts.networkServers[0].ID), ValidateNetworkServerAccess(Update, ts.networkServers[0].ID), ValidateNetworkServerAccess(Delete, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Read, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can not update or delete",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Update, ts.networkServers[0].ID), ValidateNetworkServerAccess(Delete, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not read, update or delete",
				Validators: []ValidatorFunc{ValidateNetworkServerAccess(Read, ts.networkServers[0].ID), ValidateNetworkServerAccess(Update, ts.networkServers[0].ID), ValidateNetworkServerAccess(Delete, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestOrganization() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}

	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
		ID             int64
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for i, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)
		orgUsers[i].ID = id

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	applications := []storage.Application{
		{OrganizationID: ts.organizations[0].ID, Name: "application-1", ServiceProfileID: serviceProfileIDs[0]},
	}
	for i := range applications {
		assert.NoError(storage.CreateApplication(context.Background(), storage.DB(), &applications[i]))
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "app", ApplicationID: &applications[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	ts.T().Run("IsOrganizationAdmin", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users are",
				Validators: []ValidatorFunc{ValidateIsOrganizationAdmin(ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users are",
				Validators: []ValidatorFunc{ValidateIsOrganizationAdmin(ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "normal organization users are not",
				Validators: []ValidatorFunc{ValidateIsOrganizationAdmin(applications[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key is",
				Validators: []ValidatorFunc{ValidateIsOrganizationAdmin(ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "organization api key is",
				Validators: []ValidatorFunc{ValidateIsOrganizationAdmin(ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "application api key is not",
				Validators: []ValidatorFunc{ValidateIsOrganizationAdmin(ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("OrganizationsAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(Create), ValidateOrganizationsAccess(List)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(List)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(List)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "normal users users can list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(List)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can not create",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(Create)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "normal users can not create",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(Create)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "inactive global admin users can not create and list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(Create), ValidateOrganizationsAccess(List)},
				Claims:     Claims{Username: "inactiveAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(Create), ValidateOrganizationsAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can not create or list",
				Validators: []ValidatorFunc{ValidateOrganizationsAccess(Create), ValidateOrganizationsAccess(List)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("TestOrganizationAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Read, ts.organizations[0].ID), ValidateOrganizationAccess(Update, ts.organizations[0].ID), ValidateOrganizationAccess(Delete, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read and update",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Read, ts.organizations[0].ID), ValidateOrganizationAccess(Update, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Read, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin can not delete",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Delete, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "organization users can not update or delete",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Update, ts.organizations[0].ID), ValidateOrganizationAccess(Delete, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "normal users can not read, update or delete",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Read, ts.organizations[0].ID), ValidateOrganizationAccess(Update, ts.organizations[0].ID), ValidateOrganizationAccess(Delete, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Read, ts.organizations[0].ID), ValidateOrganizationAccess(Update, ts.organizations[0].ID), ValidateOrganizationAccess(Delete, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read and update",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Read, ts.organizations[0].ID), ValidateOrganizationAccess(Update, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can not delete",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Delete, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
			{
				Name:       "application api key can not read, update or delete",
				Validators: []ValidatorFunc{ValidateOrganizationAccess(Read, ts.organizations[0].ID), ValidateOrganizationAccess(Update, ts.organizations[0].ID), ValidateOrganizationAccess(Delete, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("ValidateOrganizationUsersAccess (DisableAssignExistingUsers=false)", func(t *testing.T) {
		DisableAssignExistingUsers = false
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not create",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "normal users can not create and list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api token can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api token can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api token can not create or list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("OrganizationUsersAccess (DisableAssignExistingUsers=true)", func(t *testing.T) {
		DisableAssignExistingUsers = true
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can not create",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api token can create and list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api token can list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api token can not create",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
			{
				Name:       "app api token can not create or list",
				Validators: []ValidatorFunc{ValidateOrganizationUsersAccess(Create, ts.organizations[0].ID), ValidateOrganizationUsersAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("OrganizationUserAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read, update or delete",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Update, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Delete, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read, update or delete",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Update, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Delete, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization user can read own user record",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization user can not read other user record",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[1].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "organization users can not update or delete",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Update, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Delete, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "normal users can not read, update or delete",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Update, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Delete, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Update, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Delete, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Update, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Delete, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateOrganizationUserAccess(Read, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Update, ts.organizations[0].ID, orgUsers[0].ID), ValidateOrganizationUserAccess(Delete, ts.organizations[0].ID, orgUsers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("OrganizationNetworkServerAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not read when the network-server is not linked to the organization",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[1].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not read",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[0].ID)},
				Claims:     Claims{Username: "org1ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can not read",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not read",
				Validators: []ValidatorFunc{ValidateOrganizationNetworkServerAccess(Read, ts.organizations[0].ID, ts.networkServers[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestServiceProfile() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}
	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
		ID             int64
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for i, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)
		orgUsers[i].ID = id

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	ts.T().Run("ServiceProfilesAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(Create, ts.organizations[0].ID), ValidateServiceProfilesAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can list",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "any user can list when organization id = 0",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(List, 0)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can not create",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "organization users can not create",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization can not create or list",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(Create, ts.organizations[1].ID), ValidateServiceProfilesAccess(List, ts.organizations[1].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(Create, ts.organizations[0].ID), ValidateServiceProfilesAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can list",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can not create",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not create and list",
				Validators: []ValidatorFunc{ValidateServiceProfilesAccess(Create, ts.organizations[0].ID), ValidateServiceProfilesAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("ServiceProfileAccess", func(t *testing.T) {
		id := serviceProfileIDs[0]

		tests := []validatorTest{
			{
				Name:       "global admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Read, id), ValidateServiceProfileAccess(Update, id), ValidateServiceProfileAccess(Delete, id)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Read, id)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Read, id)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can not update or delete",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Update, id), ValidateServiceProfileAccess(Delete, id)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: false,
			},
			{
				Name:       "organization users can not update or delete",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Update, id), ValidateServiceProfileAccess(Delete, id)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not read, update or delete",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Read, id), ValidateServiceProfileAccess(Update, id), ValidateServiceProfileAccess(Delete, id)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Read, id), ValidateServiceProfileAccess(Update, id), ValidateServiceProfileAccess(Delete, id)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Read, id)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can not update or delete",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Update, id), ValidateServiceProfileAccess(Delete, id)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: false,
			},
			{
				Name:       "other api key can not read, update or delete",
				Validators: []ValidatorFunc{ValidateServiceProfileAccess(Read, id), ValidateServiceProfileAccess(Update, id), ValidateServiceProfileAccess(Delete, id)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestMulticastGroup() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}
	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
		ID             int64
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for i, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)
		orgUsers[i].ID = id

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
		{Name: "test-sp-2", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	multicastGroups := []storage.MulticastGroup{
		{Name: "mg-1", ServiceProfileID: serviceProfileIDs[0]},
		{Name: "mg-2", ServiceProfileID: serviceProfileIDs[1]},
	}
	var multicastGroupsIDs []uuid.UUID
	for i := range multicastGroups {
		assert.NoError(storage.CreateMulticastGroup(context.Background(), storage.DB(), &multicastGroups[i]))
		mgID, _ := uuid.FromBytes(multicastGroups[i].MulticastGroup.Id)
		multicastGroupsIDs = append(multicastGroupsIDs, mgID)
	}

	ts.T().Run("MulticastGroupsAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can create and list",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(Create, ts.organizations[0].ID), ValidateMulticastGroupsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create and list",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(Create, ts.organizations[0].ID), ValidateMulticastGroupsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can list",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not create",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(Create, ts.organizations[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not create or list",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(Create, ts.organizations[0].ID), ValidateMulticastGroupsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create and list",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(Create, ts.organizations[0].ID), ValidateMulticastGroupsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can create and list",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(Create, ts.organizations[0].ID), ValidateMulticastGroupsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "other api key can not create or list",
				Validators: []ValidatorFunc{ValidateMulticastGroupsAccess(Create, ts.organizations[0].ID), ValidateMulticastGroupsAccess(List, ts.organizations[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("MulticastGroupAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin users can read, update and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Update, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can read, update, and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Update, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can read",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Read, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can not update and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Update, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization users can not read, update and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Update, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Update, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read, update and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Update, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "other api key can not read, update or delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Update, multicastGroupsIDs[0]), ValidateMulticastGroupAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("MulticastGroupQueueAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin user can create, read, list and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupQueueAccess(Create, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(List, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin users can create, read, list and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupQueueAccess(Create, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(List, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization users can create, read, list and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupQueueAccess(Create, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(List, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "non-organization users can not create, list and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupQueueAccess(Create, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(List, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create, read, list and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupQueueAccess(Create, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(List, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can create, read, list and delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupQueueAccess(Create, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(List, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "other api key can not create, read list or delete",
				Validators: []ValidatorFunc{ValidateMulticastGroupQueueAccess(Create, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Read, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(List, multicastGroupsIDs[0]), ValidateMulticastGroupQueueAccess(Delete, multicastGroupsIDs[0])},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func (ts *ValidatorTestSuite) TestFUOTA() {
	assert := require.New(ts.T())

	users := []struct {
		username string
		isActive bool
		isAdmin  bool
	}{
		{username: "activeAdmin", isActive: true, isAdmin: true},
		{username: "inactiveAdmin", isActive: false, isAdmin: true},
		{username: "activeUser", isActive: true, isAdmin: false},
		{username: "inactiveUser", isActive: false, isAdmin: false},
	}

	for _, user := range users {
		_, err := ts.CreateUser(user.username, user.isActive, user.isAdmin)
		assert.NoError(err)
	}

	orgUsers := []struct {
		organizationID int64
		username       string
		isAdmin        bool
		isDeviceAdmin  bool
		isGatewayAdmin bool
	}{
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[0].ID, username: "org0ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUser", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserAdmin", isAdmin: true, isDeviceAdmin: false, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserDeviceAdmin", isAdmin: false, isDeviceAdmin: true, isGatewayAdmin: false},
		{organizationID: ts.organizations[1].ID, username: "org1ActiveUserGatewayAdmin", isAdmin: false, isDeviceAdmin: false, isGatewayAdmin: true},
	}

	for _, orgUser := range orgUsers {
		id, err := ts.CreateUser(orgUser.username, true, false)
		assert.NoError(err)

		err = storage.CreateOrganizationUser(context.Background(), storage.DB(), orgUser.organizationID, id, orgUser.isAdmin, orgUser.isDeviceAdmin, orgUser.isGatewayAdmin)
		assert.NoError(err)
	}

	var serviceProfileIDs []uuid.UUID
	serviceProfiles := []storage.ServiceProfile{
		{Name: "test-sp-1", NetworkServerID: ts.networkServers[0].ID, OrganizationID: ts.organizations[0].ID},
	}
	for i := range serviceProfiles {
		assert.NoError(storage.CreateServiceProfile(context.Background(), storage.DB(), &serviceProfiles[i]))
		id, _ := uuid.FromBytes(serviceProfiles[i].ServiceProfile.Id)
		serviceProfileIDs = append(serviceProfileIDs, id)
	}

	applications := []storage.Application{
		{OrganizationID: ts.organizations[0].ID, Name: "application-1", ServiceProfileID: serviceProfileIDs[0]},
	}
	for i := range applications {
		assert.NoError(storage.CreateApplication(context.Background(), storage.DB(), &applications[i]))
	}

	apiKeys := []storage.APIKey{
		{Name: "admin", IsAdmin: true},
		{Name: "org", OrganizationID: &ts.organizations[0].ID},
		{Name: "app", ApplicationID: &applications[0].ID},
		{Name: "empty"},
	}
	for i := range apiKeys {
		_, err := storage.CreateAPIKey(context.Background(), storage.DB(), &apiKeys[i])
		assert.NoError(err)
	}

	deviceProfiles := []storage.DeviceProfile{
		{Name: "test-dp-1", OrganizationID: ts.organizations[0].ID, NetworkServerID: ts.networkServers[0].ID},
	}
	var deviceProfilesIDs []uuid.UUID
	for i := range deviceProfiles {
		assert.NoError(storage.CreateDeviceProfile(context.Background(), storage.DB(), &deviceProfiles[i]))
		dpID, _ := uuid.FromBytes(deviceProfiles[i].DeviceProfile.Id)
		deviceProfilesIDs = append(deviceProfilesIDs, dpID)
	}

	devices := []storage.Device{
		{DevEUI: lorawan.EUI64{1, 1, 1, 1, 1, 1, 1, 1}, Name: "test-1", ApplicationID: applications[0].ID, DeviceProfileID: deviceProfilesIDs[0]},
	}
	for i := range devices {
		assert.NoError(storage.CreateDevice(context.Background(), storage.DB(), &devices[i]))
	}

	fuotaDeployments := []storage.FUOTADeployment{
		{Name: "test-fuota", GroupType: storage.FUOTADeploymentGroupTypeC, DR: 5, Frequency: 868100000, Payload: []byte{1, 2, 3, 4}, FragSize: 20, MulticastTimeout: 1, UnicastTimeout: time.Second},
	}
	for i := range fuotaDeployments {
		assert.NoError(storage.CreateFUOTADeploymentForDevice(context.Background(), storage.DB(), &fuotaDeployments[i], devices[0].DevEUI))
	}

	ts.T().Run("FUOTADeploymentAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin user can read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin can read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization user can read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: true,
			},
			{
				Name:       "non-organization user can not read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "other api key can not read",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentAccess(Read, fuotaDeployments[0].ID)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})

	ts.T().Run("FUOTADeploymentsAccess", func(t *testing.T) {
		tests := []validatorTest{
			{
				Name:       "global admin user can create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{Username: "activeAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization admin user can create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{Username: "org0ActiveUserAdmin"},
				ExpectedOK: true,
			},
			{
				Name:       "organization user can not create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{Username: "org0ActiveUser"},
				ExpectedOK: false,
			},
			{
				Name:       "non-organization user can not create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{Username: "activeUser"},
				ExpectedOK: false,
			},
			{
				Name:       "admin api key can create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{APIKeyID: apiKeys[0].ID},
				ExpectedOK: true,
			},
			{
				Name:       "org api key can create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{APIKeyID: apiKeys[1].ID},
				ExpectedOK: true,
			},
			{
				Name:       "app api key can create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{APIKeyID: apiKeys[2].ID},
				ExpectedOK: true,
			},
			{
				Name:       "other api key can not create",
				Validators: []ValidatorFunc{ValidateFUOTADeploymentsAccess(Create, applications[0].ID, lorawan.EUI64{}), ValidateFUOTADeploymentsAccess(Create, 0, devices[0].DevEUI)},
				Claims:     Claims{APIKeyID: apiKeys[3].ID},
				ExpectedOK: false,
			},
		}

		ts.RunTests(t, tests)
	})
}

func TestValidators(t *testing.T) {
	suite.Run(t, new(ValidatorTestSuite))
}
