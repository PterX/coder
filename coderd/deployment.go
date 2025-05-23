package coderd

import (
	"context"
	"net/http"

	"github.com/kylecarbs/aisdk-go"

	"github.com/coder/coder/v2/coderd/httpapi"
	"github.com/coder/coder/v2/coderd/rbac"
	"github.com/coder/coder/v2/coderd/rbac/policy"
	"github.com/coder/coder/v2/codersdk"
)

// @Summary Get deployment config
// @ID get-deployment-config
// @Security CoderSessionToken
// @Produce json
// @Tags General
// @Success 200 {object} codersdk.DeploymentConfig
// @Router /deployment/config [get]
func (api *API) deploymentValues(rw http.ResponseWriter, r *http.Request) {
	if !api.Authorize(r, policy.ActionRead, rbac.ResourceDeploymentConfig) {
		httpapi.Forbidden(rw)
		return
	}

	values, err := api.DeploymentValues.WithoutSecrets()
	if err != nil {
		httpapi.InternalServerError(rw, err)
		return
	}

	httpapi.Write(
		r.Context(), rw, http.StatusOK,
		codersdk.DeploymentConfig{
			Values:  values,
			Options: api.DeploymentOptions,
		},
	)
}

// @Summary Get deployment stats
// @ID get-deployment-stats
// @Security CoderSessionToken
// @Produce json
// @Tags General
// @Success 200 {object} codersdk.DeploymentStats
// @Router /deployment/stats [get]
func (api *API) deploymentStats(rw http.ResponseWriter, r *http.Request) {
	if !api.Authorize(r, policy.ActionRead, rbac.ResourceDeploymentStats) {
		httpapi.Forbidden(rw)
		return
	}

	stats, ok := api.metricsCache.DeploymentStats()
	if !ok {
		httpapi.Write(r.Context(), rw, http.StatusBadRequest, codersdk.Response{
			Message: "Deployment stats are still processing!",
		})
		return
	}

	httpapi.Write(r.Context(), rw, http.StatusOK, stats)
}

// @Summary Build info
// @ID build-info
// @Produce json
// @Tags General
// @Success 200 {object} codersdk.BuildInfoResponse
// @Router /buildinfo [get]
func buildInfoHandler(resp codersdk.BuildInfoResponse) http.HandlerFunc {
	// This is in a handler so that we can generate API docs info.
	return func(rw http.ResponseWriter, r *http.Request) {
		httpapi.Write(r.Context(), rw, http.StatusOK, resp)
	}
}

// @Summary SSH Config
// @ID ssh-config
// @Security CoderSessionToken
// @Produce json
// @Tags General
// @Success 200 {object} codersdk.SSHConfigResponse
// @Router /deployment/ssh [get]
func (api *API) sshConfig(rw http.ResponseWriter, r *http.Request) {
	httpapi.Write(r.Context(), rw, http.StatusOK, api.SSHConfig)
}

type LanguageModel struct {
	codersdk.LanguageModel
	Provider func(ctx context.Context, messages []aisdk.Message, thinking bool) (aisdk.DataStream, error)
}

// @Summary Get language models
// @ID get-language-models
// @Security CoderSessionToken
// @Produce json
// @Tags General
// @Success 200 {object} codersdk.LanguageModelConfig
// @Router /deployment/llms [get]
func (api *API) deploymentLLMs(rw http.ResponseWriter, r *http.Request) {
	models := make([]codersdk.LanguageModel, 0, len(api.LanguageModels))
	for _, model := range api.LanguageModels {
		models = append(models, model.LanguageModel)
	}
	httpapi.Write(r.Context(), rw, http.StatusOK, codersdk.LanguageModelConfig{
		Models: models,
	})
}
