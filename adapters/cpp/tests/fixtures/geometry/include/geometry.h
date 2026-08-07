#pragma once

namespace geometry {

struct Shape {
  virtual ~Shape() = default;
  virtual int area() const = 0;
};

class Square final : public Shape {
 public:
  explicit Square(int width);
  int area() const override;

 private:
  int width_;
};

int scaled_area(const Shape& shape, int scale);

}  // namespace geometry
