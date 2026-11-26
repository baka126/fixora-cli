package pdbfix

import (
	"errors"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"regexp"
	"strconv"
	"strings"
)

type Effect struct{ Replicas, RequiredHealthy, AllowedDisruptions int32 }

var percentPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)%$`)

func validBudget(v *intstr.IntOrString) bool {
	if v == nil {
		return false
	}
	if v.Type == intstr.Int {
		return v.IntVal >= 0 && v.StrVal == ""
	}
	if v.Type != intstr.String || !percentPattern.MatchString(v.StrVal) {
		return false
	}
	n, e := strconv.Atoi(strings.TrimSuffix(v.StrVal, "%"))
	return e == nil && n <= 100
}
func EvaluateBudget(s policyv1.PodDisruptionBudgetSpec, n, healthy int32) (Effect, error) {
	e := Effect{Replicas: n}
	if n < 1 || healthy < 0 || healthy > n {
		return e, errors.New("positive desired replicas and valid healthy count required")
	}
	if (s.MinAvailable == nil) == (s.MaxUnavailable == nil) {
		return e, errors.New("exactly one PDB budget required")
	}
	v := s.MinAvailable
	if v == nil {
		v = s.MaxUnavailable
	}
	if !validBudget(v) {
		return e, errors.New("budget must be nonnegative integer or percentage 0–100%")
	}
	scaled, err := intstr.GetScaledValueFromIntOrPercent(v, int(n), true)
	if err != nil {
		return e, err
	}
	if scaled < 0 || int64(scaled) > 1<<31-1 {
		return e, errors.New("budget overflow")
	}
	required := int64(scaled)
	if s.MaxUnavailable != nil {
		required = int64(n) - required
		if required < 0 {
			required = 0
		}
	}
	e.RequiredHealthy = int32(required)
	allowed := int64(healthy) - required
	if allowed > 0 {
		e.AllowedDisruptions = int32(allowed)
	}
	return e, nil
}
