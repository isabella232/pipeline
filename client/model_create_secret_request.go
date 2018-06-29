/*
 * Pipeline API
 *
 * Pipeline v0.3.0 swagger
 *
 * API version: 0.3.0
 * Contact: info@banzaicloud.com
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package client

type CreateSecretRequest struct {
	Name    string                 `json:"name"`
	Type    string                 `json:"type"`
	Tags    []string               `json:"tags,omitempty"`
	Version int32                  `json:"version,omitempty"`
	Values  map[string]interface{} `json:"values"`
}
