/*
 * Copyright (C) 2017 Dgraph Labs, Inc. and Contributors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package types

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/golang/geo/s2"
	"github.com/twpayne/go-geom"

	"github.com/dgraph-io/dgraph/protos"
	"github.com/dgraph-io/dgraph/x"
)

// QueryType indicates the type of geo query.
type QueryType byte

const (
	// QueryTypeWithin finds all points that are within the given geometry
	QueryTypeWithin QueryType = iota
	// QueryTypeContains finds all polygons that contain the given point
	QueryTypeContains
	// QueryTypeIntersects finds all objects that intersect the given geometry
	QueryTypeIntersects
	// QueryTypeNear finds all points that are within the given distance from the given point.
	QueryTypeNear
)

// GeoQueryData is internal data used by the geo query filter to additionally filter the geometries.
type GeoQueryData struct {
	pt    *s2.Point  // If not nil, the input data was a point
	loops []*s2.Loop // If not empty, the input data was a polygon/multipolygon.
	cap   *s2.Cap    // If not nil, the cap to be used for a near query
	qtype QueryType
}

// IsGeoFunc returns if a function is of geo type.
func IsGeoFunc(str string) bool {
	switch str {
	case "near", "contains", "within", "intersects":
		return true
	}

	return false
}

// GetGeoTokens returns the corresponding index keys based on the type
// of function.
func GetGeoTokens(funcArgs []string) ([]string, *GeoQueryData, error) {
	x.AssertTruef(len(funcArgs) > 1, "Invalid function")
	funcName := strings.ToLower(funcArgs[0])
	switch funcName {
	case "near":
		if len(funcArgs) != 4 {
			return nil, nil, x.Errorf("near function requires 2 arguments, but got %d",
				len(funcArgs))
		}
		maxDist, err := strconv.ParseFloat(funcArgs[3], 64)
		if err != nil {
			return nil, nil, x.Wrapf(err, "Error while converting distance to float")
		}
		if maxDist < 0 {
			return nil, nil, x.Errorf("Distance cannot be negative")
		}
		g, err := convertToGeom(funcArgs[2])
		if err != nil {
			return nil, nil, err
		}
		return queryTokensGeo(QueryTypeNear, g, maxDist)
	case "within":
		if len(funcArgs) != 3 {
			return nil, nil, x.Errorf("within function requires 1 arguments, but got %d",
				len(funcArgs))
		}
		g, err := convertToGeom(funcArgs[2])
		if err != nil {
			return nil, nil, err
		}
		return queryTokensGeo(QueryTypeWithin, g, 0.0)
	case "contains":
		if len(funcArgs) != 3 {
			return nil, nil, x.Errorf("contains function requires 1 arguments, but got %d",
				len(funcArgs))
		}
		g, err := convertToGeom(funcArgs[2])
		if err != nil {
			return nil, nil, err
		}
		return queryTokensGeo(QueryTypeContains, g, 0.0)
	case "intersects":
		if len(funcArgs) != 3 {
			return nil, nil, x.Errorf("intersects function requires 1 arguments, but got %d",
				len(funcArgs))
		}
		g, err := convertToGeom(funcArgs[2])
		if err != nil {
			return nil, nil, err
		}
		return queryTokensGeo(QueryTypeIntersects, g, 0.0)
	default:
		return nil, nil, x.Errorf("Invalid geo function")
	}
}

// queryTokensGeo returns the tokens to be used to look up the geo index for a given filter.
// qt is the type of Geo query - near/intersects/contains/within
// g is the geom.T representation of the input. It could be a point/polygon/multipolygon.
// maxDistance is distance in metres, only used for near query.
func queryTokensGeo(qt QueryType, g geom.T, maxDistance float64) ([]string, *GeoQueryData, error) {
	var loops []*s2.Loop
	var pt *s2.Point
	var err error
	switch v := g.(type) {
	case *geom.Point:
		// Get s2 point from geom.Point.
		p := pointFromPoint(v)
		pt = &p

	case *geom.Polygon:
		l, err := loopFromPolygon(v)
		if err != nil {
			return nil, nil, err
		}
		loops = append(loops, l)

	case *geom.MultiPolygon:
		// We get a loop for each polygon.
		for i := 0; i < v.NumPolygons(); i++ {
			l, err := loopFromPolygon(v.Polygon(i))
			if err != nil {
				return nil, nil, err
			}
			loops = append(loops, l)
		}

	default:
		return nil, nil, x.Errorf("Cannot query using a geometry of type %T", v)
	}

	x.AssertTruef(len(loops) > 0 || pt != nil, "We should have a point or a loop.")

	parents, cover, err := indexCells(g)
	if err != nil {
		return nil, nil, err
	}

	switch qt {
	case QueryTypeWithin:
		// For a within query we only need to look at the objects whose parents match our cover.
		// So we take our cover and prefix with the parentPrefix to look in the index.
		if len(loops) == 0 {
			return nil, nil, x.Errorf("Require a polygon for within query")
		}
		toks := createTokens(cover, parentPrefix)
		return toks, &GeoQueryData{loops: loops, qtype: qt}, nil

	case QueryTypeContains:
		// For a contains query, we only need to look at the objects whose cover matches our
		// parents. So we take our parents and prefix with the coverPrefix to look in the index.
		return createTokens(parents, coverPrefix), &GeoQueryData{pt: pt, loops: loops, qtype: qt}, nil

	case QueryTypeNear:
		if len(loops) > 0 {
			return nil, nil, x.Errorf("Cannot use a polygon in a near query")
		}
		return nearQueryKeys(*pt, maxDistance)

	case QueryTypeIntersects:
		// An intersects query is as the name suggests all the entities which intersect with the
		// given region. So we look at all the objects whose parents match our cover as well as
		// all the objects whose cover matches our parents.
		if len(loops) == 0 {
			return nil, nil, x.Errorf("Require a polygon for intersects query")
		}
		toks := parentCoverTokens(parents, cover)
		return toks, &GeoQueryData{loops: loops, qtype: qt}, nil

	default:
		return nil, nil, x.Errorf("Unknown query type")
	}
}

// nearQueryKeys creates a QueryKeys object for a near query.
func nearQueryKeys(pt s2.Point, d float64) ([]string, *GeoQueryData, error) {
	if d <= 0 {
		return nil, nil, x.Errorf("Invalid max distance specified for a near query")
	}
	a := EarthAngle(d)
	c := s2.CapFromCenterAngle(pt, a)
	cu := indexCellsForCap(c)
	// A near query is similar to within, where we are looking for points within the cap. So we need
	// all objects whose parents match the cover of the cap.
	return createTokens(cu, parentPrefix), &GeoQueryData{cap: &c, qtype: QueryTypeNear}, nil
}

// MatchesFilter applies the query filter to a geo value
func (q GeoQueryData) MatchesFilter(g geom.T) bool {
	switch q.qtype {
	case QueryTypeWithin:
		return q.isWithin(g)
	case QueryTypeContains:
		return q.contains(g)
	case QueryTypeIntersects:
		return q.intersects(g)
	case QueryTypeNear:
		if q.cap == nil {
			return false
		}
		return q.isWithin(g)
	}
	return false
}

func withinCapPolygon(g1 *s2.Loop, g2 *s2.Cap) bool {
	return g2.Contains(g1.CapBound())
}

func loopWithinMultiloops(l *s2.Loop, loops []*s2.Loop) bool {
	for _, s2loop := range loops {
		if Contains(s2loop, l) {
			return true
		}
	}
	return false
}

// returns true if the geometry represented by g is within the given loop or cap
func (q GeoQueryData) isWithin(g geom.T) bool {
	x.AssertTruef(q.pt != nil || len(q.loops) > 0 || q.cap != nil, "At least a point, loop or cap should be defined.")
	switch geometry := g.(type) {
	case *geom.Point:
		s2pt := pointFromPoint(geometry)
		if q.pt != nil {
			return q.pt.ApproxEqual(s2pt)
		}

		if len(q.loops) > 0 {
			for _, l := range q.loops {
				if l.ContainsPoint(s2pt) {
					return true
				}
			}
			return false
		}
		return q.cap.ContainsPoint(s2pt)
	case *geom.Polygon:
		s2loop, err := loopFromPolygon(geometry)
		if err != nil {
			return false
		}
		if len(q.loops) > 0 {
			for _, l := range q.loops {
				if Contains(l, s2loop) {
					return true
				}
			}
			return false
		}
		if q.cap != nil {
			return withinCapPolygon(s2loop, q.cap)
		}
	case *geom.MultiPolygon:
		// We check each polygon in the multipolygon should be within some loop of q.loops.
		if len(q.loops) > 0 {
			for i := 0; i < geometry.NumPolygons(); i++ {
				s2loop, err := loopFromPolygon(geometry.Polygon(i))
				if err != nil {
					return false
				}
				if !loopWithinMultiloops(s2loop, q.loops) {
					return false
				}
			}
			return true
		}

		if q.cap != nil {
			for i := 0; i < geometry.NumPolygons(); i++ {
				p := geometry.Polygon(i)
				s2loop, err := loopFromPolygon(p)
				if err != nil {
					return false
				}
				if !withinCapPolygon(s2loop, q.cap) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func multiPolygonContainsLoop(g *geom.MultiPolygon, l *s2.Loop) bool {
	for i := 0; i < g.NumPolygons(); i++ {
		p := g.Polygon(i)
		s2loop, err := loopFromPolygon(p)
		if err != nil {
			return false
		}
		if Contains(s2loop, l) {
			return true
		}
	}
	return false
}

// returns true if the geometry represented by g contains the given point/polygon.
// g is the geom.T representation of the value which is the stored in the DB.
func (q GeoQueryData) contains(g geom.T) bool {
	x.AssertTruef(q.pt != nil || len(q.loops) > 0, "At least a point or loop should be defined.")
	switch v := g.(type) {
	case *geom.Polygon:
		s2loop, err := loopFromPolygon(v)
		if err != nil {
			return false
		}
		if q.pt != nil {
			return s2loop.ContainsPoint(*q.pt)
		}

		// Input could be a multipolygon, in which q.loops would have more than 1 loop. Each loop
		// in the query should be part of the s2loop.
		for _, l := range q.loops {
			if !Contains(s2loop, l) {
				return false
			}
		}
		return true
	case *geom.MultiPolygon:
		if q.pt != nil {
			for i := 0; i < v.NumPolygons(); i++ {
				p := v.Polygon(i)
				s2loop, err := loopFromPolygon(p)
				if err != nil {
					return false
				}
				if s2loop.ContainsPoint(*q.pt) {
					return true
				}
			}
		}

		if len(q.loops) > 0 {
			// All the loops that are part of the query should be part of some loop of v.
			for _, l := range q.loops {
				if !multiPolygonContainsLoop(v, l) {
					return false
				}
			}
			return true
		}

		return false
	default:
		// We will only consider polygons for contains queries.
		return false
	}
}

// returns true if the geometry represented by uid/attr intersects the given loop or point
func (q GeoQueryData) intersects(g geom.T) bool {
	x.AssertTruef(len(q.loops) > 0, "Loop should be defined for intersects.")
	switch v := g.(type) {
	case *geom.Point:
		p := pointFromPoint(v)
		// else loop is not nil
		for _, l := range q.loops {
			if l.ContainsPoint(p) {
				return true
			}
		}
		return false

	case *geom.Polygon:
		l, err := loopFromPolygon(v)
		if err != nil {
			return false
		}
		for _, loop := range q.loops {
			if Intersects(l, loop) {
				return true
			}
		}
		return false
	case *geom.MultiPolygon:
		// We must compare all polygons in g with those in the query.
		for i := 0; i < v.NumPolygons(); i++ {
			l, err := loopFromPolygon(v.Polygon(i))
			if err != nil {
				return false
			}
			for _, loop := range q.loops {
				if Intersects(l, loop) {
					return true
				}
			}
		}
		return false
	default:
		// A type that we don't know how to handle.
		return false
	}
}

// FilterGeoUids filters the uids based on the corresponding values and GeoQueryData.
// The uids are obtained through the index. This second pass ensures that the values actually
// match the query criteria.
func FilterGeoUids(uids *protos.List, values []*protos.TaskValue, q *GeoQueryData) *protos.List {
	x.AssertTruef(len(values) == len(uids.Uids), "lengths not matching")
	rv := &protos.List{}
	for i := 0; i < len(values); i++ {
		valBytes := values[i].Val
		if bytes.Equal(valBytes, nil) {
			continue
		}
		vType := values[i].ValType
		if TypeID(vType) != GeoID {
			continue
		}
		src := ValueForType(BinaryID)
		src.Value = valBytes
		gc, err := Convert(src, GeoID)
		if err != nil {
			continue
		}
		g := gc.Value.(geom.T)

		if !q.MatchesFilter(g) {
			continue
		}

		// we matched the geo filter, add the uid to the list
		rv.Uids = append(rv.Uids, uids.Uids[i])
	}
	return rv
}
