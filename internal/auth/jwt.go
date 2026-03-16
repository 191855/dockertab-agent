package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Service struct {
	secret     []byte
	issuer     string
	expiration time.Duration
}

type Claims struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	jwt.RegisteredClaims
}

func NewService(secret string) *Service {
	return &Service{
		secret:     []byte(secret),
		issuer:     "dockertab-agent",
		expiration: 180 * 24 * time.Hour,
	}
}

func (s *Service) GenerateToken(deviceID, deviceName string) (string, error) {
	now := time.Now()
	claims := Claims{
		DeviceID:   deviceID,
		DeviceName: deviceName,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   deviceID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.expiration)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

func (s *Service) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}
