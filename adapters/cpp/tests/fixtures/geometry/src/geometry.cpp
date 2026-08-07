#include "geometry.h"

namespace geometry {

Square::Square(int width) : width_(width) {}

int Square::area() const { return width_ * width_; }

int scaled_area(const Shape& shape, int scale) {
  return shape.area() * scale;
}

}  // namespace geometry
